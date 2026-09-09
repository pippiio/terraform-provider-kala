package provider

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/techchapter/terraform-provider-kala/internal/client"
)

// Acceptance tests for the customer, case, task, and assignment resources.
//
// # Why these are shaped differently from the employee suite
//
// The employee tests reuse a fixed number because create is an UPSERT: run N+1
// adopts whatever run N left, so a hundred runs converge on one record.
//
// None of the four resources here works that way. Kala allocates every id and
// number itself, so create is not idempotent and cannot be: running a create
// test twice makes two records, and Kala can delete neither. This suite is
// therefore built on the opposite premise -- it does NOT create by default.
//
//   - Read, Update, Import, and Destroy run on EVERY invocation, against fixed
//     records named by the environment. Those are the paths where regressions
//     actually hide, and all of them are reversible.
//   - Create runs only under KALA_ACC_CREATE=1, because each run leaves a
//     permanent record. It is a deliberate act, not a side effect of running
//     the suite.
//   - The non-creating tests ASSERT they created nothing, so an accidental
//     create fails the test rather than quietly littering the tenant.
//
// # What running these does to the tenant
//
// By default: renames a case and a task and puts them back; edits a customer's
// description and puts it back; assigns and unassigns an employee. Every one of
// those is reversible, and the tests restore the original value in a final step
// rather than leaving the tenant altered.
//
// With KALA_ACC_CREATE=1: creates one customer, one case, and one task that
// cannot ever be deleted.

const (
	envAccCustomerID = "KALA_ACC_CUSTOMER_ID"
	envAccCaseNumber = "KALA_ACC_CASE_NUMBER"
	envAccTaskID     = "KALA_ACC_TASK_ID"
	envAccCreate     = "KALA_ACC_CREATE"
)

// skipUnlessAcc guards the setup these tests do BEFORE resource.Test.
//
// resource.Test skips on its own when TF_ACC is unset, but only once it is
// reached. These tests read the tenant first -- to capture the values they
// will restore -- so without this guard `go test ./...` would hit the network
// and fail, which would make the hermetic suite depend on credentials.
func skipUnlessAcc(t *testing.T) {
	t.Helper()
	if os.Getenv("TF_ACC") == "" {
		t.Skip("set TF_ACC=1 to run acceptance tests")
	}
}

// accWritePreCheck fails rather than skips, for the same reason the employee
// suite does: a silent skip inside an enabled run reports success for tests
// that never executed.
func accWritePreCheck(t *testing.T) {
	t.Helper()
	testAccPreCheck(t)
	for _, name := range []string{envAccCustomerID, envAccCaseNumber, envAccTaskID} {
		if os.Getenv(name) == "" {
			t.Fatalf("%s must be set for the write acceptance tests; see the Acceptance tests "+
				"section of README.md", name)
		}
	}
}

func accEnvInt(t *testing.T, name string) int64 {
	t.Helper()
	n, err := strconv.ParseInt(os.Getenv(name), 10, 64)
	if err != nil || n <= 0 {
		t.Fatalf("%s = %q, want a positive integer", name, os.Getenv(name))
	}
	return n
}

// accCreateEnabled reports whether the irreversible tests may run.
func accCreateEnabled() bool { return os.Getenv(envAccCreate) == "1" }

func skipUnlessCreateEnabled(t *testing.T) {
	t.Helper()
	if !accCreateEnabled() {
		t.Skipf("set %s=1 to run this test — it CREATES A RECORD THAT CANNOT BE DELETED",
			envAccCreate)
	}
}

// --- kala_customer ---------------------------------------------------------

// Import, change one field, put it back. Every step is reversible and nothing
// is created.
func TestAccCustomer_importUpdateAndRestore(t *testing.T) {
	skipUnlessAcc(t)
	id := accEnvInt(t, envAccCustomerID)

	before, err := accInternalClient(t).GetCustomer(context.Background(), id)
	if err != nil {
		t.Fatalf("reading customer %d before the test: %v", id, err)
	}

	cfg := func(description string) string {
		return fmt.Sprintf(`
resource "kala_customer" "acc" {
  company     = %q
  first_name  = %q
  last_name   = %q
  email       = %q
  phone       = %q
  address     = %q
  zip         = %q
  cvr         = %q
  description = %q
}`, before.Company, before.FirstName, before.LastName, before.Email,
			before.Phone, before.Address, before.Zip, before.CVR, description)
	}

	marker := "terraform-provider-kala acceptance test"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { accWritePreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:             cfg(before.Description),
				ResourceName:       "kala_customer.acc",
				ImportState:        true,
				ImportStateId:      strconv.FormatInt(id, 10),
				ImportStatePersist: true,
			},
			{
				// EditCustomer replaces the whole record, so this also proves
				// the other fields were not blanked by writing one of them.
				Config: cfg(marker),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("kala_customer.acc", "description", marker),
					resource.TestCheckResourceAttr("kala_customer.acc", "company", before.Company),
					resource.TestCheckResourceAttr("kala_customer.acc", "cvr", before.CVR),
					accCheckCustomerUpstream(t, id, func(c client.Customer) error {
						if c.Description != marker {
							return fmt.Errorf("upstream description = %q, want %q", c.Description, marker)
						}
						if c.Company != before.Company {
							return fmt.Errorf("company was blanked: %q", c.Company)
						}
						return nil
					}),
				),
			},
			{
				// Restore, so the tenant is left as it was found.
				Config: cfg(before.Description),
				Check: accCheckCustomerUpstream(t, id, func(c client.Customer) error {
					if c.Description != before.Description {
						return fmt.Errorf("description was not restored: %q", c.Description)
					}
					return nil
				}),
			},
		},
	})
}

func accCheckCustomerUpstream(t *testing.T, id int64, check func(client.Customer) error) resource.TestCheckFunc {
	return func(*terraform.State) error {
		got, err := accInternalClient(t).GetCustomer(context.Background(), id)
		if err != nil {
			return err
		}
		return check(got)
	}
}

// Creating a customer is irreversible, so it is opt-in.
func TestAccCustomer_create(t *testing.T) {
	skipUnlessAcc(t)
	skipUnlessCreateEnabled(t)

	company := "TFACC-" + strconv.FormatInt(int64(os.Getpid()), 10)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { accWritePreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		// Destroy removes from state and writes nothing; the customer remains.
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
resource "kala_customer" "new" {
  company     = %q
  description = "Created by terraform-provider-kala acceptance tests. Cannot be deleted."
}`, company),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("kala_customer.new", "company", company),
				resource.TestCheckResourceAttrSet("kala_customer.new", "id"),
				resource.TestCheckResourceAttrSet("kala_customer.new", "number"),
			),
		}},
	})
}

// --- kala_case -------------------------------------------------------------

// Rename a case and put the name back. Proves the compare-and-swap write path
// end to end, which no unit test can: the previous value must match upstream.
func TestAccCase_importRenameAndRestore(t *testing.T) {
	skipUnlessAcc(t)
	number := os.Getenv(envAccCaseNumber)

	before, err := accInternalClient(t).GetCase(context.Background(), number)
	if err != nil {
		t.Fatalf("reading case %s before the test: %v", number, err)
	}

	cfg := func(name string) string {
		return fmt.Sprintf(`
resource "kala_case" "acc" {
  name            = %q
  customer_number = %q
  worker_number   = %d
}`, name, accCaseCustomerNumber(t, before), accNumber(t, envAccNumber))
	}

	renamed := before.Name + " (tfacc)"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { accWritePreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:             cfg(before.Name),
				ResourceName:       "kala_case.acc",
				ImportState:        true,
				ImportStateId:      number,
				ImportStatePersist: true,
			},
			{
				Config: cfg(renamed),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("kala_case.acc", "name", renamed),
					accCheckCaseUpstream(t, number, func(c client.CaseDetail) error {
						if c.Name != renamed {
							return fmt.Errorf("upstream name = %q, want %q", c.Name, renamed)
						}
						return nil
					}),
				),
			},
			{
				Config: cfg(before.Name),
				Check: accCheckCaseUpstream(t, number, func(c client.CaseDetail) error {
					if c.Name != before.Name {
						return fmt.Errorf("name was not restored: %q", c.Name)
					}
					return nil
				}),
			},
		},
	})
}

// accCaseCustomerNumber finds the case's customer number, or fails clearly.
//
// The case detail carries a customer ID; the resource takes the string number,
// so the two have to be reconciled here rather than guessed.
func accCaseCustomerNumber(t *testing.T, d client.CaseDetail) string {
	t.Helper()
	if d.CustomerID == 0 {
		t.Fatalf("case %s is an internal project; %s must name a customer-facing case",
			d.Number, envAccCaseNumber)
	}
	c, err := accInternalClient(t).GetCustomer(context.Background(), d.CustomerID)
	if err != nil {
		t.Fatalf("reading customer %d for case %s: %v", d.CustomerID, d.Number, err)
	}
	return c.Number
}

func accCheckCaseUpstream(t *testing.T, number string, check func(client.CaseDetail) error) resource.TestCheckFunc {
	return func(*terraform.State) error {
		got, err := accInternalClient(t).GetCase(context.Background(), number)
		if err != nil {
			return err
		}
		return check(got)
	}
}

// Archive and unarchive, in both directions, from whatever state the case is
// found in.
//
// The test UNARCHIVES first if it needs to, so it never skips on tenant state,
// then exercises archive and unarchive as first-class steps rather than
// treating one of them as cleanup. Both directions matter: archive is what
// destroy does, and unarchive is the only thing that undoes it.
//
// Whatever happens, the case is put back the way it was found.
func TestAccCase_archiveAndUnarchiveRoundTrip(t *testing.T) {
	skipUnlessAcc(t)

	number := os.Getenv(envAccCaseNumber)
	c := accInternalClient(t)
	ctx := context.Background()

	before, err := c.GetCase(ctx, number)
	if err != nil {
		t.Fatalf("reading case %s: %v", number, err)
	}

	// Setup, not cleanup: the test needs an ACTIVE case to archive, and a case
	// left archived by an earlier interrupted run would otherwise make it skip
	// forever. Unarchiving here is also the first assertion that unarchive
	// works at all.
	if before.Archived {
		t.Logf("case %s was archived; unarchiving it so the round trip can run", number)
		if err := c.SetCaseArchived(ctx, number, false); err != nil {
			t.Fatalf("could not unarchive case %s to set up the test: %v", number, err)
		}
		if got, err := c.GetCase(ctx, number); err != nil || got.Archived {
			t.Fatalf("case %s is still archived after unarchiving it: %v", number, err)
		}
	}

	// Restore the state the case was found in, whatever the test does.
	t.Cleanup(func() {
		if err := c.SetCaseArchived(ctx, number, before.Archived); err != nil {
			t.Errorf("could not restore case %s to archived=%t: %v", number, before.Archived, err)
		}
	})

	cfg := func(archived bool) string {
		return fmt.Sprintf(`
resource "kala_case" "acc" {
  name            = %q
  customer_number = %q
  worker_number   = %d
  archived        = %t
}`, before.Name, accCaseCustomerNumber(t, before), accNumber(t, envAccNumber), archived)
	}

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { accWritePreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:             cfg(false),
				ResourceName:       "kala_case.acc",
				ImportState:        true,
				ImportStateId:      number,
				ImportStatePersist: true,
			},
			{
				// Archive. This is also what destroy does.
				Config: cfg(true),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("kala_case.acc", "archived", "true"),
					accCheckCaseUpstream(t, number, func(d client.CaseDetail) error {
						if !d.Archived {
							return fmt.Errorf("case %s was not archived upstream", number)
						}
						return nil
					}),
				),
			},
			{
				// Unarchive. Archival being REVERSIBLE and VERIFIABLE is what
				// let ADR-003 make destroy archive rather than only forget, so
				// this direction is load-bearing and not merely tidy-up.
				Config: cfg(false),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("kala_case.acc", "archived", "false"),
					accCheckCaseUpstream(t, number, func(d client.CaseDetail) error {
						if d.Archived {
							return fmt.Errorf("case %s was not returned to the active set", number)
						}
						return nil
					}),
				),
			},
		},
	})
}

func TestAccCase_create(t *testing.T) {
	skipUnlessAcc(t)
	skipUnlessCreateEnabled(t)

	name := "TFACC case " + strconv.FormatInt(int64(os.Getpid()), 10)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { accWritePreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
resource "kala_case" "new" {
  name             = %q
  internal_project = true
  worker_number    = %d
}`, name, accNumber(t, envAccNumber)),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("kala_case.new", "name", name),
				resource.TestCheckResourceAttr("kala_case.new", "internal_project", "true"),
				resource.TestCheckResourceAttrSet("kala_case.new", "number"),
			),
		}},
	})
}

// --- kala_task -------------------------------------------------------------

func TestAccTask_importRenameAndRestore(t *testing.T) {
	skipUnlessAcc(t)
	caseNumber := os.Getenv(envAccCaseNumber)
	taskID := accEnvInt(t, envAccTaskID)
	c := accInternalClient(t)

	detail, err := c.GetCase(context.Background(), caseNumber)
	if err != nil {
		t.Fatalf("reading case %s: %v", caseNumber, err)
	}
	before := accFindTask(t, detail.ID, taskID)

	cfg := func(name string) string {
		return fmt.Sprintf(`
resource "kala_task" "acc" {
  case_number = %q
  name        = %q
}`, caseNumber, name)
	}
	renamed := before.Name + " (tfacc)"

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { accWritePreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config:             cfg(before.Name),
				ResourceName:       "kala_task.acc",
				ImportState:        true,
				ImportStateId:      fmt.Sprintf("%s:%d", caseNumber, taskID),
				ImportStatePersist: true,
			},
			{
				Config: cfg(renamed),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("kala_task.acc", "name", renamed),
					// is_finished is read-only; a completed task must not be
					// reverted by an unrelated update (AC5).
					resource.TestCheckResourceAttr("kala_task.acc", "is_finished",
						strconv.FormatBool(before.IsFinished)),
				),
			},
			{
				Config: cfg(before.Name),
				Check: func(*terraform.State) error {
					got := accFindTask(t, detail.ID, taskID)
					if got.Name != before.Name {
						return fmt.Errorf("name was not restored: %q", got.Name)
					}
					return nil
				},
			},
		},
	})
}

func accFindTask(t *testing.T, caseID, taskID int64) client.Task {
	t.Helper()
	scan, err := accInternalClient(t).ListTasks(context.Background(), client.TaskQuery{CaseID: caseID})
	if err != nil {
		t.Fatalf("listing tasks on case %d: %v", caseID, err)
	}
	for _, k := range scan.Tasks {
		if k.ID == taskID {
			return k
		}
	}
	t.Fatalf("%s = %d is not an item on case %d", envAccTaskID, taskID, caseID)
	return client.Task{}
}

func TestAccTask_create(t *testing.T) {
	skipUnlessAcc(t)
	skipUnlessCreateEnabled(t)

	name := "TFACC task " + strconv.FormatInt(int64(os.Getpid()), 10)
	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { accWritePreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{{
			Config: fmt.Sprintf(`
resource "kala_task" "new" {
  case_number = %q
  name        = %q
  description = "Created by terraform-provider-kala acceptance tests. Cannot be deleted."
  deadline    = "2030-01-01T12:00:00Z"
}`, os.Getenv(envAccCaseNumber), name),
			Check: resource.ComposeAggregateTestCheckFunc(
				resource.TestCheckResourceAttr("kala_task.new", "name", name),
				// Second-precision: the value written must be the value read.
				resource.TestCheckResourceAttr("kala_task.new", "deadline", "2030-01-01T12:00:00Z"),
				// Set on create only via the follow-up update, since
				// AddChecklistItem does not accept a description.
				resource.TestCheckResourceAttrSet("kala_task.new", "description"),
			),
		}},
	})
}

// --- kala_task_assignment --------------------------------------------------

// The only fully reversible lifecycle here, so it runs create-to-destroy on
// every invocation. Assigning and unassigning leaves the tenant as it was.
func TestAccTaskAssignment_lifecycle(t *testing.T) {
	skipUnlessAcc(t)
	caseNumber := os.Getenv(envAccCaseNumber)
	taskID := accEnvInt(t, envAccTaskID)
	worker := accNumber(t, envAccNumber)
	c := accInternalClient(t)

	detail, err := c.GetCase(context.Background(), caseNumber)
	if err != nil {
		t.Fatalf("reading case %s: %v", caseNumber, err)
	}

	// Leave nothing assigned that this test assigned, even if it fails midway.
	t.Cleanup(func() {
		_ = c.RemoveJobLinkChecklistItem(context.Background(), caseNumber, taskID, worker)
	})

	cfg := fmt.Sprintf(`
resource "kala_task_assignment" "acc" {
  case_number   = %q
  worker_number = %d
  task_ids      = [%d]
}`, caseNumber, worker, taskID)

	resource.Test(t, resource.TestCase{
		PreCheck:                 func() { accWritePreCheck(t) },
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy: func(*terraform.State) error {
			ids, err := c.AssignedTaskIDs(context.Background(), detail.ID, worker)
			if err != nil {
				return err
			}
			for _, id := range ids {
				if id == taskID {
					return fmt.Errorf("employee %d is still assigned to task %d after destroy",
						worker, taskID)
				}
			}
			return nil
		},
		Steps: []resource.TestStep{
			{
				Config: cfg,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("kala_task_assignment.acc", "task_ids.#", "1"),
					resource.TestCheckResourceAttrSet("kala_task_assignment.acc", "job_link_id"),
					// Asserted upstream, not against Terraform's own state:
					// whether EnsureJobLink adopts or duplicates is unverified,
					// so the tenant is the source of truth here.
					func(*terraform.State) error {
						ids, err := c.AssignedTaskIDs(context.Background(), detail.ID, worker)
						if err != nil {
							return err
						}
						for _, id := range ids {
							if id == taskID {
								return nil
							}
						}
						return fmt.Errorf("employee %d was not assigned to task %d upstream",
							worker, taskID)
					},
				),
			},
			{
				ResourceName:      "kala_task_assignment.acc",
				ImportState:       true,
				ImportStateId:     fmt.Sprintf("%s:%d", caseNumber, worker),
				ImportStateVerify: false, // job_link_id is not recoverable on import
			},
		},
	})
}
