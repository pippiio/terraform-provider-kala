// Story: Checklist-item (task) writes on the internal app API
//
// Input:  a TaskInput describing the item, plus a cliId for updates.
// Process:
//   1. Create via POST /api/AddChecklistItem/, which allocates and returns
//      {"Id":N,...} -- capital I, alone among the response fields.
//   2. Update via POST /api/UpdateChecklistItem/, which is a FULL-RECORD
//      REPLACE keyed on cliId (int) AND caseNr (string) in one body. Omission
//      BLANKS, so callers must read-modify-write.
//   3. Truncate deadlines to SECOND precision. Kala's create response echoes
//      the millisecond value it was given, but the list read returns it 2 ms
//      later (characterised 2026-09-08) -- so a millisecond-precision
//      attribute could never produce an empty second plan.
//   4. Verify by read-back through ListTasks, absorbing the write/read name
//      asymmetry: the field is `name` on the way out and `text` on the way
//      back.
//
// Output: the persisted Task.
//
// Dependencies: internalAPI.authedRequest, ListTasks.
// Side effects: CREATES AND MUTATES REAL CHECKLIST ITEMS. Kala has no delete.

package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// TaskInput is the writable surface of a checklist item.
//
// Description is absent from the CREATE body and present on UPDATE, so a task
// configured with one needs create-then-update. StatusName, IsFinished, and
// the hour/assignment fields are absent deliberately: completion is
// transactional (a product Non-Goal) and assignment is a separate resource.
type TaskInput struct {
	// CaseNumber addresses the case on WRITES; CaseID addresses it on READS.
	// Kala uses different identifiers for the two directions, and carrying
	// both avoids a GetCase round-trip on every write to translate between
	// them. Callers have both: the case list and detail return each.
	CaseNumber  string
	CaseID      int64
	Name        string
	Description string

	// Deadline is truncated to the second before being sent. See step 3.
	Deadline *time.Time

	NoteRequired  bool
	ImageRequired bool

	InvoiceMode string
	PriceFixed  *int

	ExternalQualityCheckRequired    bool
	ExternalQualityCheckTemplateURL string
	ExternalQualityCheckType        string
}

// taskDeadlineLayout is RFC 3339 at SECOND precision.
//
// Kala's create response echoes the millisecond value it was given, but the
// list read returns it 2 ms later (characterised 2026-09-08). Sending
// sub-second precision would therefore guarantee a non-empty second plan
// forever, so the client truncates on the way out and TruncateSecond() on the
// way back. A checklist deadline does not need millisecond resolution, and
// exposing a precision the API cannot preserve would publish a defect as a
// feature.
const taskDeadlineLayout = "2006-01-02T15:04:05Z"

func (in TaskInput) writeBody(cliID *int64) map[string]any {
	body := map[string]any{
		"caseNr":                          in.CaseNumber,
		"name":                            in.Name,
		"noteRequired":                    in.NoteRequired,
		"imageRequired":                   in.ImageRequired,
		"externalQualityCheckRequired":    in.ExternalQualityCheckRequired,
		"externalQualityCheckTemplateUrl": in.ExternalQualityCheckTemplateURL,
		"externalQualityCheckType":        in.ExternalQualityCheckType,
		"invoiceMode":                     in.InvoiceMode,
		"priceFixed":                      in.PriceFixed,
	}
	if in.Deadline != nil {
		body["deadline"] = in.Deadline.UTC().Truncate(time.Second).Format(taskDeadlineLayout)
	} else {
		body["deadline"] = nil
	}

	// description and normTime are UPDATE-only: AddChecklistItem does not
	// accept them, and sending them on create would be sending fields the
	// endpoint has never been observed to take.
	if cliID != nil {
		body["cliId"] = *cliID
		body["description"] = nullIfEmpty(in.Description)
		body["normTime"] = nil
	}
	return body
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// readTaskBack finds an item in its case's list.
//
// The list is the verification source rather than the write response because
// Kala's create and update payloads have DIFFERENT shapes and both are thinner
// than the list -- neither is a reliable picture of the record.
func (c *internalAPI) readTaskBack(ctx context.Context, caseID, id int64) (Task, error) {
	scan, err := c.ListTasks(ctx, TaskQuery{CaseID: caseID})
	if err != nil {
		return Task{}, err
	}
	for _, k := range scan.Tasks {
		if k.ID == id {
			return k, nil
		}
	}
	if !scan.Complete() {
		return Task{}, fmt.Errorf(
			"kala: task %d was not in a read covering %d of %d items, so its absence is unproven",
			id, scan.Fetched, scan.Total)
	}
	return Task{}, fmt.Errorf("kala: task %d on case %d: %w", id, caseID, ErrNotFound)
}

// matches reports whether the persisted item carries what was asked for.
// Deadlines are compared at second precision, for the reason above.
func (in TaskInput) matches(got Task) bool {
	if in.Name != "" && in.Name != got.Name {
		return false
	}
	if in.Deadline != nil {
		if got.Deadline == nil {
			return false
		}
		want := in.Deadline.UTC().Truncate(time.Second)
		if !got.Deadline.UTC().Truncate(time.Second).Equal(want) {
			return false
		}
	}
	return true
}

func (c *internalAPI) writeTask(
	ctx context.Context, endpoint string, in TaskInput, cliID *int64,
) (Task, error) {
	raw, err := json.Marshal(in.writeBody(cliID))
	if err != nil {
		return Task{}, fmt.Errorf("kala: encoding task %q: %w", in.Name, err)
	}

	resp, err := c.authedRequest(ctx, http.MethodPost, endpoint, raw, contentTypeHeader)
	if err != nil {
		return Task{}, err
	}

	// `Id` is capitalised, alone among the response fields.
	var allocated struct {
		ID int64 `json:"Id"`
	}
	if err := json.Unmarshal(resp, &allocated); err != nil {
		return Task{}, fmt.Errorf("%w: %s response: %v", ErrDecode, endpoint, err)
	}
	if allocated.ID == 0 {
		return Task{}, fmt.Errorf(
			"kala: %s reported success but returned no item id, so the record cannot be "+
				"identified", endpoint)
	}

	got, err := c.readTaskBack(ctx, in.CaseID, allocated.ID)
	if err != nil {
		// The item exists and Kala has no delete, so its id travels with the
		// error rather than being discarded.
		return Task{ID: allocated.ID}, fmt.Errorf(
			"kala: task %d was written but could not be read back: %w", allocated.ID, err)
	}
	if !in.matches(got) {
		return Task{ID: allocated.ID}, fmt.Errorf(
			"kala: task %d reported a successful write but the record does not match what was "+
				"sent; the write was not applied", allocated.ID)
	}

	// Truncate on the way back as well as on the way out. Kala's list returns
	// the deadline a couple of milliseconds later than it was written, so
	// returning the raw value would put drift into Terraform state and produce
	// a non-empty second plan.
	if got.Deadline != nil {
		t := got.Deadline.UTC().Truncate(time.Second)
		got.Deadline = &t
	}
	return got, nil
}

// CreateTask creates a checklist item.
//
// A task configured with a description needs a follow-up UpdateTask: the
// create endpoint does not accept one.
func (c *internalAPI) CreateTask(ctx context.Context, in TaskInput) (Task, error) {
	created, err := c.writeTask(ctx, "/api/AddChecklistItem/", in, nil)
	if err != nil || in.Description == "" {
		return created, err
	}
	return c.UpdateTask(ctx, created.ID, in)
}

// UpdateTask replaces a checklist item. FULL-RECORD REPLACE -- callers must
// pass everything the item should hold, not just what changed.
func (c *internalAPI) UpdateTask(ctx context.Context, cliID int64, in TaskInput) (Task, error) {
	return c.writeTask(ctx, "/api/UpdateChecklistItem/", in, &cliID)
}
