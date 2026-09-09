package client

import (
	"reflect"
	"strings"
	"testing"
)

// Guards against the single most repeated defect of this codebase: a test mock
// that serves a JSON key the client does not actually decode.
//
// It happened five times while the customer/case/task resources were written:
//
//	customerPhone      where the API sends customersTelephone
//	archived           where the client sends archivedJobs
//	text               where the task LIST sends name
//	a missing detail endpoint the write path reads back through
//	a paginated mock that ignored `page` and never ran out
//
// Every one produced a test that passed against a mock the real API would have
// rejected, which is precisely the failure mode that let the two
// GetAllJobsSimplePaged defects reach a live tenant.
//
// A written rule would not have caught any of them -- each was typed from
// memory while writing a test about being careful. So the keys mocks depend on
// are asserted here against the struct tags themselves. If a wire struct is
// renamed, this fails and names the mock that has to follow.
//
// Add an entry whenever a mock hard-codes a key. The cost is one line; the
// alternative is a green test suite that proves nothing.

// jsonKeys returns every JSON tag name on a struct, including embedded ones.
func jsonKeys(t *testing.T, v any) map[string]bool {
	t.Helper()
	out := map[string]bool{}

	var walk func(reflect.Type)
	walk = func(rt reflect.Type) {
		if rt.Kind() == reflect.Pointer {
			rt = rt.Elem()
		}
		if rt.Kind() != reflect.Struct {
			return
		}
		for i := 0; i < rt.NumField(); i++ {
			f := rt.Field(i)
			if f.Anonymous {
				walk(f.Type)
				continue
			}
			tag := f.Tag.Get("json")
			if tag == "" || tag == "-" {
				continue
			}
			out[strings.Split(tag, ",")[0]] = true
		}
	}
	walk(reflect.TypeOf(v))
	return out
}

func TestWireKeys_MocksDependOnKeysThatActuallyExist(t *testing.T) {
	cases := []struct {
		name string
		typ  any
		keys []string
		why  string
	}{
		{
			name: "wireCustomer",
			typ:  wireCustomer{},
			keys: []string{"id", "number", "company", "cvr", "email", "phone", "description", "ean", "caseCount"},
			why:  "customer read-back verification and the write mocks",
		},
		{
			name: "wireCase",
			typ:  wireCase{},
			keys: []string{"caseId", "caseNumber", "caseName", "customersCompany"},
			why:  "case list mocks",
		},
		{
			name: "wireCaseDetail",
			typ:  wireCaseDetail{},
			// customersTelephone is the one that bit: the mock invented
			// "customerPhone", so read-back checked a field that never existed.
			keys: []string{"customersTelephone", "customersEmail", "customersCompany", "address", "zip"},
			why:  "case field-setter read-back",
		},
		{
			name: "wireTask",
			typ:  wireTask{},
			// `name`, not `text`. The WRITE RESPONSES say text; the LIST says
			// name, and the list is what verification reads.
			keys: []string{"Id", "name", "deadline", "caseId", "caseNr", "workersAssigned", "respWorkerNr"},
			why:  "task write read-back and assignment reads",
		},
		{
			name: "wireCasesPage",
			typ:  wireCasesPage{},
			keys: []string{"cases", "totalCount"},
			why:  "case list envelope",
		},
		{
			name: "wireTasksPage",
			typ:  wireTasksPage{},
			keys: []string{"items", "totalCount"},
			why:  "task list envelope",
		},
		{
			name: "wireCustomersPage",
			typ:  wireCustomersPage{},
			keys: []string{"customers", "totalCount"},
			why:  "customer list envelope",
		},
		{
			name: "wireJobLink",
			typ:  wireJobLink{},
			keys: []string{"id", "caseId", "caseNr", "workerNr", "checklistIds"},
			why:  "job link creation; note the response says `id` and the request says `jobLinkId`",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			have := jsonKeys(t, tc.typ)
			for _, k := range tc.keys {
				if !have[k] {
					t.Errorf("%s has no JSON key %q, but mocks for %s serve it.\n"+
						"Either the struct tag changed and the mocks must follow, or the key "+
						"was typed from memory and is wrong.\nActual keys: %v",
						tc.name, k, tc.why, sortedKeys(have))
				}
			}
		})
	}
}

// The archived flag on the case-list REQUEST, which a mock read as "archived"
// and the client sends as "archivedJobs" -- so the mock served the wrong set
// forever and a verification silently passed.
func TestWireKeys_CaseListRequestSpellsTheArchivedFlagCorrectly(t *testing.T) {
	have := jsonKeys(t, listCasesBody{})
	if !have["archivedJobs"] {
		t.Errorf("the case list request has no %q key; mocks that switch on it will serve the "+
			"wrong set. Actual keys: %v", "archivedJobs", sortedKeys(have))
	}
	if have["archived"] {
		t.Error("an `archived` key now exists too — check which one the client actually sends")
	}
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
