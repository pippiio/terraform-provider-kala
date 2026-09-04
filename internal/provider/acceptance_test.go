package provider

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/providerserver"
	"github.com/hashicorp/terraform-plugin-go/tfprotov6"
	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"

	"github.com/techchapter/terraform-provider-kala/internal/client"
)

// Acceptance tests run real Terraform against a real Kala tenant. They are
// gated on TF_ACC and skip otherwise, so `go test ./...` stays hermetic.
//
// # Why these reuse a fixed employee number
//
// Kala cannot delete an employee. A suite that allocated a fresh number per run
// would leave a permanent record behind every time it executed, and the tenant
// would accumulate them forever.
//
// The resource's own semantics make reuse safe instead: create is an upsert, so
// a number already present is adopted rather than rejected, and destroy
// deactivates rather than deletes. Running the same test a hundred times
// therefore converges on exactly one record per configured number — created
// once, then adopted and reactivated on every subsequent run.
//
// That is why KALA_ACC_EMPLOYEE_NUMBER has no default. Guessing a number would
// mean writing to whichever real person happens to hold it.
//
// # What running these does to the tenant
//
// They create (once) and then repeatedly deactivate and reactivate the
// employees named by the variables below. They never send a welcome email:
// every configuration here sets send_welcome_email = false, because mail
// reaches a real person and cannot be recalled.

const (
	envAccNumber    = "KALA_ACC_EMPLOYEE_NUMBER"
	envAccAltNumber = "KALA_ACC_EMPLOYEE_NUMBER_ALT"
	envAccEmail     = "KALA_ACC_EMAIL"
)

// testAccProtoV6ProviderFactories serves the provider in-process, so the tests
// exercise the same code the plugin binary would.
var testAccProtoV6ProviderFactories = map[string]func() (tfprotov6.ProviderServer, error){
	"kala": providerserver.NewProtocol6WithError(New("acc")()),
}

// testAccPreCheck fails — rather than skips — when TF_ACC is set but the
// environment is incomplete. A silent skip inside an enabled acceptance run
// would report success for tests that never executed.
func testAccPreCheck(t *testing.T) {
	t.Helper()

	required := []string{
		"KALA_API_KEY",
		"KALA_USERNAME",
		"KALA_PASSWORD",
		envAccNumber,
		envAccEmail,
	}
	for _, name := range required {
		if os.Getenv(name) == "" {
			t.Fatalf("%s must be set for acceptance tests; see the Acceptance tests section of README.md", name)
		}
	}
}

// accEmailFor derives a distinct address per employee number from KALA_ACC_EMAIL.
//
// The addresses MUST differ between the employees this suite touches. Kala's
// SetEmailNew silently ignores an address that already belongs to another
// worker — it returns success and leaves the old value — so a suite that used
// one shared address would fail on the second employee with a read-back
// mismatch that looks like a provider bug and is not.
//
// Observed 2026-09-04: setting worker 3 to worker 2's address reported success
// and changed nothing.
func accEmailFor(t *testing.T, number int64) string {
	t.Helper()

	base := os.Getenv(envAccEmail)
	local, domain, ok := strings.Cut(base, "@")
	if !ok {
		t.Fatalf("%s = %q, want an address of the form local@domain", envAccEmail, base)
	}
	return fmt.Sprintf("%s-acc%d@%s", local, number, domain)
}

// accCurrentEmail returns the address the employee already holds, falling back
// to a derived one when Kala reports none.
//
// Tests whose subject is NOT the email use this, so they do not depend on an
// endpoint unrelated to what they assert. That matters here: SetEmailNew
// silently declines to change the address for some employees — observed
// 2026-09-04, one worker accepted every address tried and another refused every
// address tried, active or inactive — so requiring a writable email on every
// employee the suite touches would make unrelated tests fail on tenant data
// rather than on provider behaviour.
//
// Changing the email is the lifecycle test's job, on the primary employee.
func accCurrentEmail(t *testing.T, number int64) string {
	t.Helper()

	info, err := accInternalClient(t).GetWorkerInfo(context.Background(), number)
	if err != nil || info.Email == "" {
		return accEmailFor(t, number)
	}
	return info.Email
}

// accNumber reads a required employee number from the environment.
func accNumber(t *testing.T, envVar string) int64 {
	t.Helper()

	raw := os.Getenv(envVar)
	if raw == "" {
		t.Skipf("%s is not set; skipping the test that needs a second employee number", envVar)
	}

	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n <= 0 {
		t.Fatalf("%s = %q, want a positive integer", envVar, raw)
	}
	return n
}

// accInternalClient builds an internal-API client. It is the surface the
// assertions read back through: webapiv2 does not report activation state, and
// the point of these checks is to confirm what Kala holds rather than what
// Terraform believes.
func accInternalClient(t *testing.T) client.InternalClient {
	t.Helper()

	return client.NewInternal(client.InternalConfig{
		Username: os.Getenv("KALA_USERNAME"),
		Password: os.Getenv("KALA_PASSWORD"),
	})
}

// --- configurations -------------------------------------------------------

func accConfigEmployee(number int64, name, email string, extra string) string {
	return fmt.Sprintf(`
provider "kala" {}

resource "kala_employee" "test" {
  employee_number    = %d
  name               = %q
  email              = %q
  send_welcome_email = false
%s}
`, number, name, email, extra)
}

func accConfigEmployeeWithDataSource(number int64, name, email string) string {
	return accConfigEmployee(number, name, email, "") + `
data "kala_employees" "all" {
  depends_on = [kala_employee.test]
}
`
}

// --- checks ---------------------------------------------------------------

// checkEmployeeActive asserts the employee's activation state directly against
// Kala, not against Terraform state. State agreeing with itself proves nothing.
func checkEmployeeActive(t *testing.T, number int64, want bool) resource.TestCheckFunc {
	return func(*terraform.State) error {
		w, err := accInternalClient(t).GetWorker(context.Background(), number)
		if err != nil {
			return fmt.Errorf("reading worker %d back from Kala: %w", number, err)
		}
		if w.IsValidated != want {
			return fmt.Errorf("employee %d: active = %t upstream, want %t", number, w.IsValidated, want)
		}
		return nil
	}
}

// checkEmployeeFieldUpstream asserts one profile field as Kala reports it.
func checkEmployeeFieldUpstream(t *testing.T, number int64, field string, want string) resource.TestCheckFunc {
	return func(*terraform.State) error {
		info, err := accInternalClient(t).GetWorkerInfo(context.Background(), number)
		if err != nil {
			return fmt.Errorf("reading worker info for %d: %w", number, err)
		}

		var got string
		switch field {
		case "title":
			got = info.Title
		case "department":
			got = info.Department
		case "initials":
			got = info.Initials
		case "phone":
			got = info.Phone
		case "email":
			got = info.Email
		case "name":
			got = info.Name
		default:
			return fmt.Errorf("checkEmployeeFieldUpstream: unknown field %q", field)
		}

		if got != want {
			return fmt.Errorf("employee %d: %s = %q upstream, want %q", number, field, got, want)
		}
		return nil
	}
}

// checkEmployeeBoss asserts who Kala says the employee reports to.
func checkEmployeeBoss(t *testing.T, number, wantBoss int64) resource.TestCheckFunc {
	return func(*terraform.State) error {
		info, err := accInternalClient(t).GetWorkerInfo(context.Background(), number)
		if err != nil {
			return fmt.Errorf("reading worker info for %d: %w", number, err)
		}
		if info.BossNumber != wantBoss {
			return fmt.Errorf("employee %d: boss = %d upstream, want %d",
				number, info.BossNumber, wantBoss)
		}
		return nil
	}
}

// checkDestroyDeactivates is the CheckDestroy for every employee test.
//
// The usual "the resource is gone" assertion cannot hold here: Kala has no
// delete. What destroy must guarantee is that the employee is INACTIVE, and
// that is what this verifies. Asserting absence instead would either fail
// always or, if written loosely, pass without the deactivation happening.
func checkDestroyDeactivates(t *testing.T, number int64) resource.TestCheckFunc {
	return func(*terraform.State) error {
		w, err := accInternalClient(t).GetWorker(context.Background(), number)
		if err != nil {
			return fmt.Errorf("reading worker %d after destroy: %w", number, err)
		}
		if w.IsValidated {
			return fmt.Errorf("employee %d is still active after destroy; "+
				"destroy must deactivate, since Kala cannot delete", number)
		}
		return nil
	}
}

// --- tests ----------------------------------------------------------------

// The core lifecycle: create (or adopt), update fields in place, and destroy
// into deactivation. Every step ends with a plan that must be empty (TF1.4).
func TestAccEmployee_lifecycle(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("acceptance test; set TF_ACC=1 to run")
	}
	testAccPreCheck(t)

	number := accNumber(t, envAccNumber)
	email := accEmailFor(t, number)
	name := "Acceptance Test Employee"

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             checkDestroyDeactivates(t, number),
		Steps: []resource.TestStep{
			{
				Config: accConfigEmployee(number, name, email, `
  title      = "Acceptance Title"
  department = "Acceptance Dept"
`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("kala_employee.test",
						"employee_number", strconv.FormatInt(number, 10)),
					resource.TestCheckResourceAttr("kala_employee.test", "active", "true"),
					resource.TestCheckResourceAttr("kala_employee.test", "title", "Acceptance Title"),
					resource.TestCheckResourceAttr("kala_employee.test", "department", "Acceptance Dept"),
					resource.TestCheckResourceAttrSet("kala_employee.test", "worker_id"),
					// The write is only real if Kala agrees.
					checkEmployeeActive(t, number, true),
					checkEmployeeFieldUpstream(t, number, "title", "Acceptance Title"),
					checkEmployeeFieldUpstream(t, number, "department", "Acceptance Dept"),
					// email is the one attribute with real drift detection, so
					// the write must be confirmed upstream like the rest.
					checkEmployeeFieldUpstream(t, number, "email", email),
				),
			},
			// An in-place update must reach Kala, not just Terraform state.
			{
				Config: accConfigEmployee(number, name, email, `
  title      = "Acceptance Title Updated"
  department = "Acceptance Dept"
  initials   = "ATE"
`),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("kala_employee.test", "title", "Acceptance Title Updated"),
					resource.TestCheckResourceAttr("kala_employee.test", "initials", "ATE"),
					checkEmployeeFieldUpstream(t, number, "title", "Acceptance Title Updated"),
					checkEmployeeFieldUpstream(t, number, "initials", "ATE"),
				),
			},
		},
	})
}

// active = false must deactivate without destroying, and setting it back to
// true must reactivate. This is the only employee field Kala lets the provider
// both read and write, so it is the only one with genuine drift detection.
func TestAccEmployee_activeTogglesDeactivation(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("acceptance test; set TF_ACC=1 to run")
	}
	testAccPreCheck(t)

	number := accNumber(t, envAccNumber)
	email := accEmailFor(t, number)
	name := "Acceptance Test Employee"

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             checkDestroyDeactivates(t, number),
		Steps: []resource.TestStep{
			{
				Config: accConfigEmployee(number, name, email, "  active = true\n"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("kala_employee.test", "active", "true"),
					checkEmployeeActive(t, number, true),
				),
			},
			{
				Config: accConfigEmployee(number, name, email, "  active = false\n"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("kala_employee.test", "active", "false"),
					checkEmployeeActive(t, number, false),
				),
			},
			// Back to active: reactivation must work, or an offboarded person
			// could never be re-onboarded.
			{
				Config: accConfigEmployee(number, name, email, "  active = true\n"),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("kala_employee.test", "active", "true"),
					checkEmployeeActive(t, number, true),
				),
			},
		},
	})
}

// Adoption is the behaviour that makes these tests repeatable at all: the
// second apply of the same number must take ownership rather than fail.
//
// It uses the alternate number so it can assert adopted = true on a record the
// preceding steps of this same test created, without depending on whether the
// primary number already existed when the suite first ran.
func TestAccEmployee_adoptsAnExistingNumber(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("acceptance test; set TF_ACC=1 to run")
	}
	testAccPreCheck(t)

	number := accNumber(t, envAccAltNumber)
	email := accCurrentEmail(t, number)
	name := "Acceptance Adoption Employee"

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             checkDestroyDeactivates(t, number),
		Steps: []resource.TestStep{
			{
				Config: accConfigEmployee(number, name, email, ""),
				Check:  checkEmployeeActive(t, number, true),
			},
			// Taint forces a destroy-then-create within the run. Destroy
			// deactivates, so the re-create must adopt AND reactivate.
			{
				Taint:  []string{"kala_employee.test"},
				Config: accConfigEmployee(number, name, email, ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("kala_employee.test", "adopted", "true"),
					resource.TestCheckResourceAttr("kala_employee.test", "active", "true"),
					checkEmployeeActive(t, number, true),
				),
			},
		},
	})
}

// Import takes the employee number as the ID. email is unimportable — no Kala
// read endpoint returns it — so it is excluded from the state comparison rather
// than the test pretending it round-trips.
func TestAccEmployee_import(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("acceptance test; set TF_ACC=1 to run")
	}
	testAccPreCheck(t)

	number := accNumber(t, envAccNumber)
	email := accEmailFor(t, number)
	name := "Acceptance Test Employee"

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             checkDestroyDeactivates(t, number),
		Steps: []resource.TestStep{
			{
				Config: accConfigEmployee(number, name, email, ""),
			},
			{
				ResourceName:  "kala_employee.test",
				ImportState:   true,
				ImportStateId: strconv.FormatInt(number, 10),
				// The resource has no "id" attribute — Kala's identifier is the
				// employee number, and inventing a synthetic id would add a
				// field with no meaning upstream. The framework must be told
				// which attribute identifies the resource instead.
				ImportStateVerifyIdentifierAttribute: "employee_number",
				ImportStateVerify:                    true,
				ImportStateVerifyIgnore: []string{
					// Creation-time only; not a property of the record.
					"send_welcome_email",
					// Import recovers the name Kala actually stores. The
					// managed state holds the CONFIGURED name, and the two
					// legitimately differ: Kala has no rename endpoint, so a
					// changed name is a warned no-op upstream. Comparing them
					// would assert a round-trip the API cannot perform.
					"name",
				},
			},
		},
	})
}

// A rename is a real write via ChangeWorkerName. The point of asserting it
// upstream is that this resource previously reported renames it never made.
func TestAccEmployee_renameIsWrittenUpstream(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("acceptance test; set TF_ACC=1 to run")
	}
	testAccPreCheck(t)

	number := accNumber(t, envAccNumber)
	email := accEmailFor(t, number)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             checkDestroyDeactivates(t, number),
		Steps: []resource.TestStep{
			{
				Config: accConfigEmployee(number, "Acceptance Test Employee", email, ""),
			},
			{
				Config: accConfigEmployee(number, "Acceptance Test Renamed", email, ""),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("kala_employee.test", "name", "Acceptance Test Renamed"),
					// State agreeing with config proves nothing here — the old
					// behaviour did exactly that while Kala kept the old name.
					checkEmployeeFieldUpstream(t, number, "name", "Acceptance Test Renamed"),
				),
			},
			// And back, so the suite leaves the record as it found it.
			{
				Config: accConfigEmployee(number, "Acceptance Test Employee", email, ""),
				Check:  checkEmployeeFieldUpstream(t, number, "name", "Acceptance Test Employee"),
			},
		},
	})
}

// boss_employee_number is written through ChangeBoss and read back from the
// nested firstBoss object, so both halves of an unusually shaped field are
// exercised against the real API.
func TestAccEmployee_bossIsWrittenUpstream(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("acceptance test; set TF_ACC=1 to run")
	}
	testAccPreCheck(t)

	number := accNumber(t, envAccNumber)
	boss := accNumber(t, envAccAltNumber)
	email := accEmailFor(t, number)
	name := "Acceptance Test Employee"

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             checkDestroyDeactivates(t, number),
		Steps: []resource.TestStep{
			{
				Config: accConfigEmployee(number, name, email,
					fmt.Sprintf("  boss_employee_number = %d\n", boss)),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("kala_employee.test",
						"boss_employee_number", strconv.FormatInt(boss, 10)),
					resource.TestCheckResourceAttrSet("kala_employee.test", "boss_name"),
					checkEmployeeBoss(t, number, boss),
				),
			},
		},
	})
}

// The data source must see the employee the resource just created, which is
// what proves the two APIs address the same person by the same number.
func TestAccEmployeesDataSource_seesTheManagedEmployee(t *testing.T) {
	if os.Getenv("TF_ACC") == "" {
		t.Skip("acceptance test; set TF_ACC=1 to run")
	}
	testAccPreCheck(t)

	number := accNumber(t, envAccNumber)
	email := accEmailFor(t, number)

	resource.Test(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		CheckDestroy:             checkDestroyDeactivates(t, number),
		Steps: []resource.TestStep{
			{
				Config: accConfigEmployeeWithDataSource(number, "Acceptance Test Employee", email),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttrSet("data.kala_employees.all", "employees.#"),
					checkDataSourceListsEmployee(number),
				),
			},
		},
	})
}

// checkDataSourceListsEmployee scans the data source's flattened state for the
// employee number, since the index within the list is not predictable.
func checkDataSourceListsEmployee(number int64) resource.TestCheckFunc {
	return func(s *terraform.State) error {
		rs, ok := s.RootModule().Resources["data.kala_employees.all"]
		if !ok {
			return fmt.Errorf("data.kala_employees.all is not in state")
		}

		want := strconv.FormatInt(number, 10)
		for key, value := range rs.Primary.Attributes {
			if strings.HasSuffix(key, ".number") && value == want {
				return nil
			}
		}
		return fmt.Errorf("employee %d does not appear in kala_employees; "+
			"the resource created them, so the data source must list them", number)
	}
}
