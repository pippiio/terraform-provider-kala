// Story: Task read path (internal app API)
//
// A Kala "task" is a CHECKLIST ITEM. It carries completion, hours, and billing
// data, which draft/product.md excludes from managed state; reading it via a
// data source is a deliberate, recorded narrowing of that Non-Goal.
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
//      it. Only respWorkerNr is used: workersAssigned was never observed
//      populated, so its element shape is unknown and guessing it would be
//      inventing a contract.
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
//               not called and is not on ARCH1.3's permitted-write list.

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

	// AssigneeWorkerNr is respWorkerNr, nil when unassigned. It is the only
	// assignee signal used: workersAssigned was never observed populated.
	AssigneeWorkerNr *int64
	AssignedToMe     bool
	CreatedBy        string

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

// ListTasks reads the checklist items of one case.
func (c *internalAPI) ListTasks(ctx context.Context, q TaskQuery) (TaskScan, error) {
	return TaskScan{}, nil // stub: RED
}

var (
	_ = bytes.TrimSpace
	_ = json.Unmarshal
	_ = fmt.Errorf
	_ = http.MethodPost
)
