// Story: Job links — the (worker, case) pairing that carries task assignment
//
// Input:  a case, a worker, and the set of checklist items they should cover.
// Process:
//   1. Ensure a job link via POST /api/NewJobLinkNoTimeCaseId/, which returns
//      the whole link including its current checklistIds. Its `id` is the
//      `jobLinkId` the next call takes -- the response spells it `id` and the
//      request spells it `jobLinkId`.
//   2. Set the covered items via POST /api/UpdateJobLinkChecklist/, which
//      takes the WHOLE array and replaces it. That maps directly onto a
//      Terraform set.
//   3. Detach one item via GET /api/RemoveJoblinkChecklistItem/ -- a write
//      performed by GET, keyed on caseNr + checklistItemId + workerNr, needing
//      no jobLinkId at all.
//
// A job link is SHARED: a worker on two items of the same case has one link
// covering both. That is why assignment is modelled as its own resource rather
// than a field on a task -- two task resources would otherwise overwrite each
// other's membership on every apply.
//
// Output: the link, or nothing on success.
//
// Dependencies: internalAPI.authedRequest, ListTasks.
// Side effects: creates and mutates real assignments.

package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

// JobLink is a worker's link to a case, and the items it covers.
type JobLink struct {
	// ID is `jobLinkId` on the request side and `id` on the response side.
	ID           int64
	CaseID       int64
	CaseNumber   string
	WorkerNr     int64
	ChecklistIDs []int64
}

type wireJobLink struct {
	ID           int64   `json:"id"`
	CaseID       int64   `json:"caseId"`
	CaseNumber   string  `json:"caseNr"`
	WorkerNr     int64   `json:"workerNr"`
	ChecklistIDs []int64 `json:"checklistIds"`
}

// EnsureJobLink links a worker to a case and returns the link.
//
// UNVERIFIED: whether calling this for an EXISTING (worker, case) pair returns
// that link or creates a duplicate. The response carrying `checklistIds`
// suggests it returns the existing one -- a freshly created link comes back
// with an empty array, so the field exists to report current state. Callers
// must therefore VERIFY the outcome by read-back rather than trust it; if the
// assumption is wrong the verification fails loudly instead of silently
// assigning the wrong worker.
func (c *internalAPI) EnsureJobLink(ctx context.Context, caseID, workerNr int64) (JobLink, error) {
	body, err := json.Marshal(map[string]any{"workerNr": workerNr, "caseId": caseID})
	if err != nil {
		return JobLink{}, fmt.Errorf("kala: encoding job link: %w", err)
	}

	raw, err := c.authedRequest(
		ctx, http.MethodPost, "/api/NewJobLinkNoTimeCaseId/", body, contentTypeHeader)
	if err != nil {
		return JobLink{}, fmt.Errorf(
			"kala: linking worker %d to case %d: %w", workerNr, caseID, err)
	}

	var wire wireJobLink
	if err := json.Unmarshal(raw, &wire); err != nil {
		return JobLink{}, fmt.Errorf("%w: job link response: %v", ErrDecode, err)
	}
	if wire.ID == 0 {
		return JobLink{}, fmt.Errorf(
			"kala: the job link was reported created but carries no id, so the items it covers "+
				"cannot be set (worker %d, case %d)", workerNr, caseID)
	}
	return JobLink{ //nolint:staticcheck // S1016: explicit mapping is intentional at the wire/domain boundary, as in models.go and customers.go — the structs are only coincidentally identical, and a conversion would silently absorb any future divergence
		ID: wire.ID, CaseID: wire.CaseID, CaseNumber: wire.CaseNumber,
		WorkerNr: wire.WorkerNr, ChecklistIDs: wire.ChecklistIDs,
	}, nil
}

// SetJobLinkChecklist replaces the set of items a job link covers.
func (c *internalAPI) SetJobLinkChecklist(ctx context.Context, jobLinkID int64, ids []int64) error {
	// Never nil: a nil slice marshals to null, and null is not an empty set.
	if ids == nil {
		ids = []int64{}
	}
	body, err := json.Marshal(map[string]any{"jobLinkId": jobLinkID, "checklistIds": ids})
	if err != nil {
		return fmt.Errorf("kala: encoding job link %d checklist: %w", jobLinkID, err)
	}
	if _, err := c.authedRequest(
		ctx, http.MethodPost, "/api/UpdateJobLinkChecklist/", body, contentTypeHeader,
	); err != nil {
		return fmt.Errorf("kala: setting the items covered by job link %d: %w", jobLinkID, err)
	}
	return nil
}

// RemoveJobLinkChecklistItem detaches one item from a worker's link.
//
// A write performed by GET, and notably it needs NO jobLinkId -- which is what
// makes destroy possible for a resource imported without one.
func (c *internalAPI) RemoveJobLinkChecklistItem(
	ctx context.Context, caseNumber string, checklistItemID, workerNr int64,
) error {
	q := url.Values{}
	q.Set("caseNr", caseNumber)
	q.Set("checklistItemId", strconv.FormatInt(checklistItemID, 10))
	q.Set("workerNr", strconv.FormatInt(workerNr, 10))

	if _, err := c.authedRequest(
		ctx, http.MethodGet, "/api/RemoveJoblinkChecklistItem/?"+q.Encode(), nil, contentTypeHeader,
	); err != nil {
		return fmt.Errorf("kala: detaching worker %d from item %d on case %s: %w",
			workerNr, checklistItemID, caseNumber, err)
	}
	return nil
}

// AssignedTaskIDs reports which of a case's items a worker is linked to.
//
// This is the READ path for assignment, and it comes from the task list's
// workersAssigned collection rather than from any job-link endpoint -- Kala
// exposes no way to read a link directly.
func (c *internalAPI) AssignedTaskIDs(ctx context.Context, caseID, workerNr int64) ([]int64, error) {
	scan, err := c.ListTasks(ctx, TaskQuery{CaseID: caseID})
	if err != nil {
		return nil, err
	}
	if !scan.Complete() {
		return nil, fmt.Errorf(
			"kala: a read covering %d of %d items on case %d cannot establish which items "+
				"worker %d is assigned to", scan.Fetched, scan.Total, caseID, workerNr)
	}

	var ids []int64
	for _, k := range scan.Tasks {
		for _, nr := range k.AssignedWorkerNrs {
			if nr == workerNr {
				ids = append(ids, k.ID)
				break
			}
		}
	}
	return ids, nil
}
