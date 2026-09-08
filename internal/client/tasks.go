// Story: Task read path (internal app API)
//
// A Kala "task" is a CHECKLIST ITEM. It carries completion, hours, and billing
// data, which is deliberately excluded from managed state; reading it via a
// data source is a deliberate, recorded narrowing of that scope.
//
// Input:  ctx, TaskQuery{CaseID (REQUIRED), Search, NameContains,
//         OnlyUnfinished, AssigneeWorkerNr, PageSize, MaxPages}
//
// Process:
//   1. Reject a zero CaseID before issuing anything. caseId is how this
//      endpoint is addressed, not an optional filter, and there is no
//      account-wide task read -- that would be one request per case.
//   2. POST /Case/GetChecklistItemsPaged/ (note: the /Case/ controller, not
//      /api/) with the filters upstream actually honours: search,
//      nameContains, and onlyUnfinished are all server-side (verified
//      2026-09-02).
//   3. An unknown caseId returns HTTP 200 with items:[] and totalCount
//      ABSENT. That is different from a real case holding no tasks, which
//      returns totalCount:0. Decode totalCount as a pointer so the two stay
//      distinguishable, and map the absent case to ErrNotFound. Defaulting a
//      missing totalCount to zero would report an unknown case as an empty
//      one, and report the read as complete.
//   4. Filter by assignee client-side; upstream has no request parameter for
//      it. respWorkerNr is the RESPONSIBLE worker; workersAssigned is the set
//      of workers linked to the item, and both are exposed. The comment here
//      previously said workersAssigned had never been observed populated --
//      true when written, because nothing had ever been assigned in the
//      development tenant, and disproved on 2026-09-08 once one was.
//   5. Parse .NET /Date(ms)/ on deadline, timeAdded, and timeFinished.
//   6. Report coverage from records RECEIVED, not records surviving the
//      client-side filter. Filtering is the caller asking for less; it must
//      never be mistaken for a truncated read.
//
// Output: TaskScan{Tasks, Total, Fetched, Pages, CaseTotal, CaseFinished}
//         where Complete() compares Fetched against upstream's totalCount.
//
// Dependencies: internalAPI.authedRequest, parseDotNetDate, ErrDecode, ErrNotFound.
// Side effects: outbound HTTPS only. AddChecklistItem exists and works but is
//               not called and is not on the permitted-write list.

package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// Task is the provider-owned representation of a Kala checklist item.
type Task struct {
	ID          int64
	Name        string
	Description string
	CaseID      int64
	CaseNumber  string
	ChecklistID int64

	// StatusName is upstream's own label and is NOT translated. Values are in
	// the tenant's language -- "Færdig" was observed. It is sourced from the
	// company-wide kanban_options setting.
	StatusName string

	// AssigneeWorkerNr is respWorkerNr, nil when unassigned -- the RESPONSIBLE
	// worker, which is distinct from the set linked to the item below.
	AssigneeWorkerNr *int64

	// AssignedWorkerNrs are the employee numbers linked to this item. Only
	// identifiers are carried: the upstream collection also holds names,
	// phone numbers, and titles, which are personal data (SEC1.5).
	AssignedWorkerNrs []int64
	AssignedToMe      bool
	CreatedBy         string

	Deadline  *time.Time
	TimeAdded *time.Time

	IsFinished   bool
	TimeFinished *time.Time
	FinishedBy   string

	RegisteredHoursTotal int
	BilledHours          int
	InvoiceMode          string
	PriceFixed           *int

	NoteRequired  bool
	ImageRequired bool
	HasImage      bool
}

// TaskQuery controls a task list read. CaseID is required.
type TaskQuery struct {
	// CaseID addresses the endpoint. Zero is rejected before any request.
	CaseID int64

	Search         string
	NameContains   string
	OnlyUnfinished bool

	// AssigneeWorkerNr filters CLIENT-SIDE; upstream offers no parameter for it.
	AssigneeWorkerNr *int64

	// AssignedWorkerNrs are the employee numbers linked to this item. Only
	// identifiers are carried: the upstream collection also holds names,
	// phone numbers, and titles, which are personal data (SEC1.5).
	AssignedWorkerNrs []int64

	PageSize int
	MaxPages int
}

// TaskScan is a task list read with the coverage it achieved.
//
// Fetched counts records RECEIVED from upstream, not records surviving the
// client-side assignee filter -- otherwise asking for one assignee's tasks
// would masquerade as a truncated read.
type TaskScan struct {
	Tasks []Task

	Total   int
	Fetched int
	Pages   int

	// CaseTotal and CaseFinished are the case-level counters upstream returns
	// alongside the page, unaffected by any filter.
	CaseTotal    int
	CaseFinished int
}

// Complete reports whether the read received everything upstream claimed.
func (s TaskScan) Complete() bool { return s.Fetched >= s.Total }

// wireTask is the checklist-item record. Note Id is capitalised while every
// sibling field is camelCase -- upstream inconsistency, not a typo here.
type wireTask struct {
	ID          int64  `json:"Id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	CaseID      int64  `json:"caseId"`
	CaseNr      string `json:"caseNr"`
	ChecklistID int64  `json:"checklistId"`
	StatusName  string `json:"statusName"`

	RespWorkerNr *int64 `json:"respWorkerNr"`

	// WorkersAssigned is the set of workers linked to this item through their
	// job link on the case. Observed 2026-09-08; carries personal data
	// (name, phone, title), of which only the identifier is mapped (SEC1.5).
	WorkersAssigned []struct {
		WorkerNr int64 `json:"workerNr"`
	} `json:"workersAssigned"`
	AssignedToMe bool   `json:"assignedToMe"`
	CreatedBy    string `json:"createdBy"`

	Deadline     string `json:"deadline"`
	TimeAdded    string `json:"timeAdded"`
	TimeFinished string `json:"timeFinished"`

	IsFinished       bool   `json:"isFinished"`
	WorkerFinishedBy string `json:"workerFinishedBy"`

	RegisteredHoursTotal int    `json:"registeredHoursTotal"`
	BilledHours          int    `json:"billedHours"`
	InvoiceMode          string `json:"invoiceMode"`
	PriceFixed           *int   `json:"priceFixed"`

	NoteRequired  bool `json:"noteRequired"`
	ImageRequired bool `json:"imageRequired"`
	HasImage      bool `json:"hasImage"`
}

// wireTasksPage is the paged envelope.
//
// TotalCount is a POINTER on purpose. An unknown caseId returns HTTP 200 with
// items:[] and totalCount absent, whereas a real case with no tasks returns
// totalCount:0. A plain int would collapse the two into "empty and complete".
type wireTasksPage struct {
	Items                    []wireTask `json:"items"`
	TotalCount               *int       `json:"totalCount"`
	CaseTotalCount           int        `json:"caseTotalCount"`
	CaseFinishedCount        int        `json:"caseFinishedCount"`
	CaseTotalNormTime        int        `json:"caseTotalNormTime"`
	CaseTotalRegisteredHours int        `json:"caseTotalRegisteredHours"`
}

// listTasksBody is the request body. There is no assignee parameter: upstream
// offers none, so assignee filtering happens client-side after the read.
type listTasksBody struct {
	CaseID         int64   `json:"caseId"`
	Page           int     `json:"page"`
	PageSize       int     `json:"pageSize"`
	Search         string  `json:"search"`
	OnlyUnfinished bool    `json:"onlyUnfinished"`
	NameContains   *string `json:"nameContains"`
	Sort           *string `json:"sort"`
}

func (w wireTask) toDomain() (Task, error) {
	t := Task{
		ID: w.ID, Name: w.Name, Description: w.Description,
		CaseID: w.CaseID, CaseNumber: w.CaseNr, ChecklistID: w.ChecklistID,
		StatusName:        w.StatusName,
		AssigneeWorkerNr:  w.RespWorkerNr,
		AssignedWorkerNrs: assignedNumbers(w.WorkersAssigned),
		AssignedToMe:      w.AssignedToMe,
		CreatedBy:         w.CreatedBy,
		IsFinished:        w.IsFinished,
		FinishedBy:        w.WorkerFinishedBy,

		RegisteredHoursTotal: w.RegisteredHoursTotal,
		BilledHours:          w.BilledHours,
		InvoiceMode:          w.InvoiceMode,
		PriceFixed:           w.PriceFixed,

		NoteRequired:  w.NoteRequired,
		ImageRequired: w.ImageRequired,
		HasImage:      w.HasImage,
	}

	for _, f := range []struct {
		raw  string
		dst  **time.Time
		name string
	}{
		{w.Deadline, &t.Deadline, "deadline"},
		{w.TimeAdded, &t.TimeAdded, "timeAdded"},
		{w.TimeFinished, &t.TimeFinished, "timeFinished"},
	} {
		parsed, err := parseDotNetDate(f.raw)
		if err != nil {
			return Task{}, fmt.Errorf("task %d %s: %w", w.ID, f.name, err)
		}
		*f.dst = parsed
	}
	return t, nil
}

// ListTasks reads the checklist items of one case.
//
// q.CaseID is required: it addresses the endpoint rather than filtering it.
// There is deliberately no account-wide task read -- that would be one request
// per case, an N+1 fan-out against an API with undocumented rate limits.
func (c *internalAPI) ListTasks(ctx context.Context, q TaskQuery) (TaskScan, error) {
	if q.CaseID <= 0 {
		return TaskScan{}, fmt.Errorf(
			"kala: a case id is required to read tasks (upstream addresses checklist items by caseId; there is no account-wide task read)")
	}

	pageSize := q.PageSize
	if pageSize <= 0 {
		pageSize = defaultPageSize
	}
	maxPages := q.MaxPages
	if maxPages <= 0 {
		maxPages = defaultMaxPages
	}

	var nameContains *string
	if q.NameContains != "" {
		nameContains = &q.NameContains
	}

	scan := TaskScan{Tasks: make([]Task, 0, pageSize)}

	for page := 0; page < maxPages; page++ {
		body, err := json.Marshal(listTasksBody{
			CaseID:         q.CaseID,
			Page:           page,
			PageSize:       pageSize,
			Search:         q.Search,
			OnlyUnfinished: q.OnlyUnfinished,
			NameContains:   nameContains,
		})
		if err != nil {
			return TaskScan{}, fmt.Errorf("kala: building task list request: %w", err)
		}

		raw, err := c.authedRequest(
			ctx, http.MethodPost, "/Case/GetChecklistItemsPaged/", body, contentTypeHeader,
		)
		if err != nil {
			return TaskScan{}, err
		}
		scan.Pages++

		if len(bytes.TrimSpace(raw)) == 0 {
			return TaskScan{}, fmt.Errorf("%w: no case with id %d", ErrNotFound, q.CaseID)
		}

		var envelope wireTasksPage
		if err := json.Unmarshal(raw, &envelope); err != nil {
			return TaskScan{}, fmt.Errorf("%w: tasks response: %v", ErrDecode, err)
		}

		// An absent totalCount is upstream's only signal that the case does not
		// exist. A real but empty case sends totalCount:0.
		if envelope.TotalCount == nil {
			return TaskScan{}, fmt.Errorf("%w: no case with id %d", ErrNotFound, q.CaseID)
		}

		scan.Total = *envelope.TotalCount
		scan.CaseTotal = envelope.CaseTotalCount
		scan.CaseFinished = envelope.CaseFinishedCount

		// Fetched counts records RECEIVED. The client-side filter below narrows
		// what the caller asked for, not what the read covered.
		scan.Fetched += len(envelope.Items)

		for _, wt := range envelope.Items {
			task, err := wt.toDomain()
			if err != nil {
				return TaskScan{}, err
			}
			if q.AssigneeWorkerNr != nil {
				if task.AssigneeWorkerNr == nil || *task.AssigneeWorkerNr != *q.AssigneeWorkerNr {
					continue
				}
			}
			scan.Tasks = append(scan.Tasks, task)
		}

		if len(envelope.Items) == 0 || scan.Fetched >= scan.Total {
			return scan, nil
		}
	}

	return scan, nil
}

// assignedNumbers reduces the workersAssigned collection to identifiers.
//
// The upstream elements also carry name, phone, and title. Those are personal
// data and are deliberately dropped at this boundary rather than carried into
// the domain and filtered later (SEC1.5).
func assignedNumbers(in []struct {
	WorkerNr int64 `json:"workerNr"`
}) []int64 {
	if len(in) == 0 {
		return nil
	}
	out := make([]int64, 0, len(in))
	for _, w := range in {
		out = append(out, w.WorkerNr)
	}
	return out
}
