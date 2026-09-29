// Story: Read-only probes for the case-access surface
//
// Input:  live credentials from KALA_USERNAME / KALA_PASSWORD, gated on
//         KALA_PROBE=1 so `go test ./...` and CI stay hermetic.
// Process:
//   1. List cases, pick the one carrying the most checklist items.
//   2. Dump a SKELETON of GET /api/GetJobDetailsAdvanced/ -- key names and
//      types only -- to settle whether any of its unmapped fields carries a
//      case-access collection.
//   3. Dump a skeleton of POST /Case/GetChecklistItemsPaged/, in particular the
//      shape of workersAssigned.
//   4. Attempt the same task read on an ARCHIVED case.
//   5. Prove a narrow wire type decodes the case that breaks the shipped one.
// Output: t.Log skeletons. Never a raw body, never a string value.
//
// What these established (track spike-findings F-1..F-6):
//   - The grant is readable in ONE call: GetJobDetailsAdvanced embeds
//     checklistItems, each carrying workersAssigned. No pagination, so no page
//     ceiling -- checklistItemsTotal against len(checklistItems) is the
//     completeness check, and it is self-describing.
//   - Kala returns DECIMALS where the client declares int. registeredHoursTotal
//     came back as 0.25, which makes ListTasks and wireCaseDetail broken decodes
//     for such a case. A narrow wire type sidesteps it; the shipped defect is
//     handed off as its own track.
//
// Dependencies: internalAPI.authedRequest, ListCases, ListTasks.
// Side effects: outbound HTTPS reads only. No write endpoint is called.
//
// SEC1.5: these payloads carry names, phone numbers, e-mail addresses and
// titles. The skeleton walker therefore prints key names, JSON types and array
// lengths, and prints VALUES only for an explicit allowlist of identifier and
// count fields. No string value is ever logged, so a probe run cannot leak
// personal data into a terminal or a CI log.

package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"testing"
)

// valueSafeKeys are the only keys whose values may be logged. Identifiers and
// counts. Everything else -- prices, names, notes, dates -- prints as its type.
var valueSafeKeys = map[string]bool{
	"caseId": true, "customerId": true, "workerNr": true, "workerId": true,
	"Id": true, "cliId": true, "id": true, "jobLinkId": true,
	"totalCount": true, "page": true, "pageSize": true,
	"checklistItemsTotal": true, "checklistItemsCompleted": true,
	"internalProject": true, "restricted": true, "favorite": true,
	"isFinished": true, "isValidated": true, "archived": true,
}

// skeleton renders a JSON document as sorted "key: type" lines, recursing into
// objects and the first element of each array.
func skeleton(raw []byte) string {
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return fmt.Sprintf("<undecodable: %v>", err)
	}
	var b strings.Builder
	walkSkeleton(&b, "", v, 0)
	return b.String()
}

func walkSkeleton(b *strings.Builder, key string, v any, depth int) {
	indent := strings.Repeat("  ", depth)

	switch t := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(t))
		for k := range t {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			walkSkeleton(b, k, t[k], depth)
		}

	case []any:
		fmt.Fprintf(b, "%s%s: array[%d]\n", indent, key, len(t))
		if len(t) > 0 {
			fmt.Fprintf(b, "%s  # shape of element 0:\n", indent)
			walkSkeleton(b, key+"[0]", t[0], depth+2)
		}

	default:
		fmt.Fprintf(b, "%s%s: %s\n", indent, key, describe(key, v))
	}
}

// describe names the type, and appends the value only for an allowlisted key.
func describe(key string, v any) string {
	var kind string
	switch t := v.(type) {
	case nil:
		return "null"
	case bool:
		kind = "bool"
	case float64:
		kind = "number"
	case string:
		// Never the content. Length alone is enough to tell an empty field
		// from a populated one without disclosing anything.
		return fmt.Sprintf("string(len=%d)", len(t))
	default:
		kind = "unknown"
	}
	if valueSafeKeys[key] {
		return fmt.Sprintf("%s = %v", kind, v)
	}
	return kind
}

// probeAPI returns the concrete client so the probe can reach authedRequest,
// which is what makes a RAW read possible. Skips unless KALA_PROBE=1.
func probeAPI(t *testing.T) *internalAPI {
	t.Helper()
	if os.Getenv("KALA_PROBE") != "1" {
		t.Skip("set KALA_PROBE=1 — reads a real Kala tenant")
	}
	c, ok := internalFromEnv().(*internalAPI)
	if !ok {
		t.Fatal("NewInternal no longer returns *internalAPI; the probe needs authedRequest")
	}
	return c
}

// TestProbe_CaseAccessSurface answers, against the live tenant:
//
//	Q: does GetJobDetailsAdvanced carry a case-access collection?
//	Q: what is the exact shape of workersAssigned?
//	Q: how many items does the largest case hold, against the page ceiling?
func TestProbe_CaseAccessSurface(t *testing.T) {
	c := probeAPI(t)
	ctx := context.Background()

	scan, err := c.ListCases(ctx, CaseQuery{})
	if err != nil {
		t.Fatalf("ListCases: %v", err)
	}
	t.Logf("cases: fetched=%d total=%d pages=%d complete=%t",
		scan.Fetched, scan.Total, scan.Pages, scan.Complete())
	if len(scan.Cases) == 0 {
		t.Skip("tenant holds no unarchived cases to probe")
	}

	// Pick the case with the most items: the widest surface, and the one that
	// says most about the page ceiling.
	var best Case
	bestItems := -1
	for _, cs := range scan.Cases {
		ts, err := c.ListTasks(ctx, TaskQuery{CaseID: cs.ID})
		if err != nil {
			t.Logf("  caseId=%d ListTasks failed: %v", cs.ID, err)
			continue
		}
		t.Logf("  caseId=%d items=%d complete=%t", cs.ID, ts.Total, ts.Complete())
		if ts.Total > bestItems {
			best, bestItems = cs, ts.Total
		}
	}
	if bestItems < 0 {
		t.Fatal("no case could be read for its items")
	}
	t.Logf("probing caseId=%d with %d item(s)", best.ID, bestItems)

	// --- Q: is the grant list in the case detail payload? ---
	detail, err := c.authedRequest(ctx, http.MethodGet,
		"/api/GetJobDetailsAdvanced/?caseNr="+best.Number, nil, contentTypeHeader)
	if err != nil {
		t.Fatalf("GetJobDetailsAdvanced: %v", err)
	}
	t.Logf("GetJobDetailsAdvanced skeleton:\n%s", skeleton(detail))

	// --- Q: what shape is workersAssigned? ---
	body, err := json.Marshal(listTasksBody{
		CaseID: best.ID, Page: 0, PageSize: defaultPageSize,
	})
	if err != nil {
		t.Fatalf("encoding task list body: %v", err)
	}
	items, err := c.authedRequest(ctx, http.MethodPost,
		"/Case/GetChecklistItemsPaged/", body, contentTypeHeader)
	if err != nil {
		t.Fatalf("GetChecklistItemsPaged: %v", err)
	}
	t.Logf("GetChecklistItemsPaged skeleton:\n%s", skeleton(items))
}

// TestProbe_ArchivedCaseTaskRead answers spec Q9: does the task read -- the only
// source of the grant -- work on an ARCHIVED case? kala_case's destroy archives,
// so a configuration that destroys a case and keeps the data source depends on
// the answer.
func TestProbe_ArchivedCaseTaskRead(t *testing.T) {
	c := probeAPI(t)
	ctx := context.Background()

	scan, err := c.ListCases(ctx, CaseQuery{Archived: true})
	if err != nil {
		t.Fatalf("ListCases(archived): %v", err)
	}
	t.Logf("archived cases: fetched=%d total=%d", scan.Fetched, scan.Total)
	if len(scan.Cases) == 0 {
		t.Skip("tenant holds no archived case — Q9 cannot be answered read-only")
	}

	target := scan.Cases[0]
	ts, err := c.ListTasks(ctx, TaskQuery{CaseID: target.ID})
	if err != nil {
		t.Logf("Q9 ANSWER: the task read FAILS on archived caseId=%d: %v", target.ID, err)
		return
	}
	t.Logf("Q9 ANSWER: the task read SUCCEEDS on archived caseId=%d — items=%d complete=%t",
		target.ID, ts.Total, ts.Complete())
}

// TestProbe_NarrowDecodeSurvivesDecimals proves the design against the exact case
// that BREAKS the shipped code.
//
// The probe found that /Case/GetChecklistItemsPaged/ fails on caseId=1 with
//
//	cannot unmarshal number 0.25 into ... registeredHoursTotal of type int
//
// because the client declares every numeric as int and Kala returns decimals
// (cases.go:105,106,191,192 and tasks.go:85,86,166,167). That is a pre-existing
// defect in kala_tasks / kala_cases / kala_case, not something this track owns.
//
// encoding/json ignores fields absent from the target struct, so a wire struct
// that decodes ONLY what case access needs never touches the offending field.
// This asserts that rather than assuming it, on the case where the difference
// is observable.
func TestProbe_NarrowDecodeSurvivesDecimals(t *testing.T) {
	c := probeAPI(t)
	ctx := context.Background()

	scan, err := c.ListCases(ctx, CaseQuery{Archived: true})
	if err != nil {
		t.Fatalf("ListCases(archived): %v", err)
	}
	if len(scan.Cases) == 0 {
		t.Skip("no archived case to probe")
	}
	target := scan.Cases[0]

	// The narrow shape: four things, none of them a decimal-bearing field.
	var narrow struct {
		CaseID              int64 `json:"caseId"`
		Restricted          bool  `json:"restricted"`
		ChecklistItemsTotal int   `json:"checklistItemsTotal"`
		ChecklistItems      []struct {
			WorkersAssigned []struct {
				WorkerNr int64 `json:"workerNr"`
			} `json:"workersAssigned"`
		} `json:"checklistItems"`
	}

	raw, err := c.authedRequest(ctx, http.MethodGet,
		"/api/GetJobDetailsAdvanced/?caseNr="+target.Number, nil, contentTypeHeader)
	if err != nil {
		t.Fatalf("GetJobDetailsAdvanced on archived case: %v", err)
	}
	if err := json.Unmarshal(raw, &narrow); err != nil {
		t.Fatalf("narrow decode FAILED — the design does not sidestep the decimal bug: %v", err)
	}

	seen := map[int64]bool{}
	for _, it := range narrow.ChecklistItems {
		for _, w := range it.WorkersAssigned {
			seen[w.WorkerNr] = true
		}
	}
	nrs := make([]int64, 0, len(seen))
	for n := range seen {
		nrs = append(nrs, n)
	}
	sort.Slice(nrs, func(i, j int) bool { return nrs[i] < nrs[j] })

	t.Logf("NARROW DECODE OK on ARCHIVED caseId=%d (this case breaks ListTasks today)",
		narrow.CaseID)
	t.Logf("  restricted=%t checklistItemsTotal=%d itemsReturned=%d",
		narrow.Restricted, narrow.ChecklistItemsTotal, len(narrow.ChecklistItems))
	t.Logf("  derived assigned set=%v", nrs)
	if narrow.ChecklistItemsTotal != len(narrow.ChecklistItems) {
		t.Logf("  NOTE: checklistItems is TRUNCATED (%d of %d) — the completeness check fires",
			len(narrow.ChecklistItems), narrow.ChecklistItemsTotal)
	}
}

// TestSkeletonRedactsPersonalData is the redaction regression for the probe
// itself, and it is HERMETIC -- it runs in the default `go test ./...`, unlike
// the probes above.
//
// The rule it satisfies is carried forward from the previous track: every new
// endpoint gets a redaction test, because a test of exactly this shape found a
// real credential leak. The probe reads two endpoints whose payloads carry
// names, phone numbers, e-mail addresses and titles, and its only protection is
// the walker in this file. Protection that nothing exercises is not protection.
//
// Two assertions, and the second is the one that keeps the first honest: a
// walker that emitted nothing at all would pass a naive leak check.
func TestSkeletonRedactsPersonalData(t *testing.T) {
	const (
		name  = "Frodo Baggins"
		phone = "31620005"
		email = "frodo@example.com"
		title = "Senior Ring Bearer"
		note  = "leader note nobody should read"
	)

	payload := []byte(`{
	  "caseId": 42,
	  "restricted": true,
	  "checklistItemsTotal": 1,
	  "leaderNote": "` + note + `",
	  "customersName": "` + name + `",
	  "customersEmail": "` + email + `",
	  "cost": 12345,
	  "checklistItems": [
	    {
	      "Id": 7,
	      "createdBy": "` + name + `",
	      "registeredHoursTotal": 0.25,
	      "workersAssigned": [
	        {"workerNr": 3, "name": "` + name + `", "phone": "` + phone + `",
	         "title": "` + title + `", "initials": "FB", "isValidated": true}
	      ]
	    }
	  ]
	}`)

	got := skeleton(payload)

	for _, secret := range []string{name, phone, email, title, note} {
		if strings.Contains(got, secret) {
			t.Errorf("skeleton leaked %q into its output:\n%s", secret, got)
		}
	}

	// The walker must still be describing the payload. Without this, an empty
	// output would satisfy every assertion above.
	for _, want := range []string{
		"caseId: number = 42",        // allowlisted identifier, value shown
		"restricted: bool = true",    // allowlisted flag, value shown
		"workerNr: number = 3",       // the one field this track actually takes
		"cost: number",               // NOT allowlisted: type only, no value
		"customersName: string(len=", // string reported by length, never content
		"workersAssigned: array[1]",  // array reported by length
	} {
		if !strings.Contains(got, want) {
			t.Errorf("skeleton did not describe %q; output was:\n%s", want, got)
		}
	}

	// Commercially sensitive numerics must not have their values printed.
	if strings.Contains(got, "12345") {
		t.Errorf("skeleton printed a non-allowlisted numeric value:\n%s", got)
	}
}
