package provider

import (
	"context"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strconv"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"

	"github.com/pippiio/terraform-provider-kala/internal/client"
)

// Acceptance tests for kala_case_access.
//
// READ-ONLY BY CONSTRUCTION. Nothing here creates or changes a record, so unlike
// the write suites it needs no fixture variables and cannot leave anything behind
// in a tenant that has no delete. It needs only KALA_USERNAME and KALA_PASSWORD.
//
// It picks its own case, and it picks it through a DIFFERENT endpoint from the
// one under test: ListTasks reads /Case/GetChecklistItemsPaged/, while
// kala_case_access reads /api/GetJobDetailsAdvanced/. Choosing the case with the
// data source itself would make "the set is non-empty" circular. Choosing it via
// the task list makes it a real check -- and asserting the two sets are EQUAL
// verifies, against the live tenant, the claim the whole design rests on: that
// the case detail payload carries the same workersAssigned as the task list.

// accCaseAccessPreCheck is narrower than testAccPreCheck on purpose: that one
// demands the employee suite's fixtures, which a read-only test has no use for.
// It still FAILS rather than skips inside an enabled run, for the same reason.
func accCaseAccessPreCheck(t *testing.T) {
	t.Helper()
	for _, name := range []string{"KALA_USERNAME", "KALA_PASSWORD"} {
		if os.Getenv(name) == "" {
			t.Fatalf("%s must be set for acceptance tests; see the Acceptance tests section of README.md", name)
		}
	}
}

// accCaseWithAssignments finds an unarchived case with at least one employee
// assigned to one of its tasks, and returns its number and the assigned set as
// the TASK LIST reports it.
//
// Cases whose task list fails to decode are passed over and logged, not fatal:
// the shipped ListTasks cannot read a case with fractional hours (tracked
// separately as fix-decimal-numeric-decode), and that defect is not what this
// test is about.
func accCaseWithAssignments(t *testing.T) (string, []int64) {
	t.Helper()
	ctx := context.Background()
	c := accInternalClient(t)

	scan, err := c.ListCases(ctx, client.CaseQuery{})
	if err != nil {
		t.Fatalf("listing cases: %v", err)
	}
	for _, cs := range scan.Cases {
		tasks, err := c.ListTasks(ctx, client.TaskQuery{CaseID: cs.ID})
		if err != nil {
			t.Logf("passing over case %s: its task list could not be read: %v", cs.Number, err)
			continue
		}
		if !tasks.Complete() {
			t.Logf("passing over case %s: its task list read was incomplete", cs.Number)
			continue
		}
		seen := map[int64]bool{}
		for _, task := range tasks.Tasks {
			for _, nr := range task.AssignedWorkerNrs {
				seen[nr] = true
			}
		}
		if len(seen) == 0 {
			continue
		}
		set := make([]int64, 0, len(seen))
		for nr := range seen {
			set = append(set, nr)
		}
		sort.Slice(set, func(i, j int) bool { return set[i] < set[j] })
		return cs.Number, set
	}

	t.Fatal("no unarchived case in this tenant has an employee assigned to any task, so there is " +
		"nothing to verify kala_case_access against. Assign an employee to a task on any case " +
		"and re-run. Failing rather than skipping: a skip inside an enabled run would report " +
		"success for a test that never executed.")
	return "", nil
}

func accConfigCaseAccess(caseNumber string) string {
	return fmt.Sprintf(`
data "kala_case_access" "test" {
  case_number = %q
}

# Surfaced as an output so that an unstable read -- the same members in a
# different order, say -- would show up as a non-empty second plan.
output "employee_numbers" {
  value = data.kala_case_access.test.employee_numbers
}
`, caseNumber)
}

// TestAccCaseAccess_matchesTheTaskList covers AC5 (an empty second plan) and
// verifies finding F-1 against the live tenant.
func TestAccCaseAccess_matchesTheTaskList(t *testing.T) {
	skipUnlessAcc(t)
	accCaseAccessPreCheck(t)

	number, want := accCaseWithAssignments(t)
	t.Logf("verifying case %s, whose task list assigns %v", number, want)

	checks := []resource.TestCheckFunc{
		resource.TestCheckResourceAttr("data.kala_case_access.test", "case_number", number),
		resource.TestCheckResourceAttrSet("data.kala_case_access.test", "case_id"),
		resource.TestCheckResourceAttrSet("data.kala_case_access.test", "restricted"),
		// EQUAL to the task list's set, not merely non-empty: two endpoints,
		// one answer.
		resource.TestCheckResourceAttr("data.kala_case_access.test",
			"employee_numbers.#", strconv.Itoa(len(want))),
	}
	for _, nr := range want {
		checks = append(checks, resource.TestCheckTypeSetElemAttr(
			"data.kala_case_access.test", "employee_numbers.*", strconv.FormatInt(nr, 10)))
	}

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: accConfigCaseAccess(number),
				Check:  resource.ComposeAggregateTestCheckFunc(checks...),
			},
			{
				// TF1.4: a second consecutive plan must be empty. Stated as its
				// own step so the guarantee is visible rather than implied by
				// the framework's post-apply plan.
				Config:             accConfigCaseAccess(number),
				PlanOnly:           true,
				ExpectNonEmptyPlan: false,
			},
		},
	})
}

// TestAccCaseAccess_unknownCaseIsAnError covers AC7 live: a case number that
// does not exist must fail the plan, never produce an empty set.
func TestAccCaseAccess_unknownCaseIsAnError(t *testing.T) {
	skipUnlessAcc(t)
	accCaseAccessPreCheck(t)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config:      accConfigCaseAccess("TFACC-NO-SUCH-CASE"),
			ExpectError: regexp.MustCompile(`Case not found`),
		}},
	})
}
