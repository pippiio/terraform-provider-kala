package provider

import (
	"context"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-go/tftypes"

	"github.com/pippiio/terraform-provider-kala/internal/client"
)

// End-to-end: a REAL internal client, against an httptest server serving the
// JSON Kala actually sends, read through the data source into Terraform state.
//
// Every other data source test here uses a fake client, and a fake cannot carry
// the bug this file exists for: the fake is built from the client's Go types, so
// if those types say int, the fake can only ever say int. The decode failure
// lived exactly at the JSON-to-Go boundary a fake skips. This path goes through
// it -- which is the test that would have caught the bug in the first place.
//
// Values are read from state as raw tftypes numbers, so these tests compile and
// assert the same way whether an attribute is declared Int64 or Float64.

func decimalKalaServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/Auth/SignIn/"):
			_, _ = w.Write([]byte(`{"globalUserId":1,"secureLoginToken":"t","companies":[{"id":4242,"name":"Rivendell"}]}`))
		case strings.HasSuffix(r.URL.Path, "/Auth/SelectCompany/"):
			_, _ = w.Write([]byte(`{"globalCompanyName":"Rivendell","token":"kauth-token"}`))

		case strings.HasSuffix(r.URL.Path, "/Case/GetChecklistItemsPaged/"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"items": []any{map[string]any{
					"Id": 5, "name": "Gutter", "caseId": 2, "caseNr": "KA-2", "checklistId": 11,
					"respWorkerNr": nil, "workersAssigned": []any{}, "assignedToMe": false,
					"isFinished": false, "invoiceMode": "REG_HOURS&SPECIAL",
					"registeredHoursTotal": 0.25, "billedHours": 1.5, "priceFixed": 549.95,
					"noteRequired": false, "imageRequired": false, "hasImage": false,
				}},
				"totalCount":               1,
				"caseTotalCount":           1,
				"caseFinishedCount":        0,
				"caseTotalNormTime":        1.5,
				"caseTotalRegisteredHours": 0.25,
			})

		case strings.HasSuffix(r.URL.Path, "/api/GetJobDetailsAdvanced/"):
			_ = json.NewEncoder(w).Encode(map[string]any{
				"caseId": 1, "caseNumber": "KA-1", "caseName": "Roof works",
				"restricted": false, "internalProject": true, "isFinished": false,
				"checklistItemsTotal": 0, "checklistItemsCompleted": 0, "checklistItems": []any{},
				"registeredHoursTotal": 1.75, "billedHours": 0.5,
				"cost": 12345.5, "sales": 19999.99, "result": 7654.49,
				"invoiced": 100.25, "uninvoiced": 0.75, "realised": 1.1,
			})

		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func decimalClient(t *testing.T) client.InternalClient {
	t.Helper()
	return client.NewInternal(client.InternalConfig{
		Endpoint: decimalKalaServer(t).URL, Username: "u", Password: "p",
		MaxRetries: 0, Timeout: 5 * time.Second,
	})
}

// numberAt reads one number out of raw state, independent of whether the schema
// declares it Int64 or Float64 -- both are tftypes.Number underneath.
func numberAt(t *testing.T, state tfsdk.State, p *tftypes.AttributePath) *big.Float {
	t.Helper()
	v, _, err := tftypes.WalkAttributePath(state.Raw, p)
	if err != nil {
		t.Fatalf("no value at %s: %v", p, err)
	}
	tv, ok := v.(tftypes.Value)
	if !ok {
		t.Fatalf("value at %s is %T, want tftypes.Value", p, v)
	}
	var f big.Float
	if err := tv.As(&f); err != nil {
		t.Fatalf("value at %s is not a number: %v", p, err)
	}
	return &f
}

func assertNumber(t *testing.T, state tfsdk.State, p *tftypes.AttributePath, want float64) {
	t.Helper()
	got := numberAt(t, state, p)
	if got.Cmp(big.NewFloat(want)) != 0 {
		t.Errorf("%s = %s, want %v — a truncated value is worse than an error", p, got.Text('g', 10), want)
	}
}

func TestKalaTasks_EndToEnd_FractionalHoursAndPrice(t *testing.T) {
	sch := tasksSchema(t)
	resp := &datasource.ReadResponse{State: tfsdk.State{Schema: sch}}
	(&tasksDataSource{client: decimalClient(t)}).Read(context.Background(),
		datasource.ReadRequest{Config: dsConfig(t, sch, map[string]tftypes.Value{
			"case_id":            tftypes.NewValue(tftypes.Number, 2),
			"include_financials": tftypes.NewValue(tftypes.Bool, true),
		})}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("kala_tasks must read a case with fractional hours; got: %v", resp.Diagnostics)
	}
	task := tftypes.NewAttributePath().WithAttributeName("tasks").WithElementKeyInt(0)
	assertNumber(t, resp.State, task.WithAttributeName("registered_hours_total"), 0.25)
	assertNumber(t, resp.State, task.WithAttributeName("billed_hours"), 1.5)
	assertNumber(t, resp.State, task.WithAttributeName("price_fixed"), 549.95)
}

func TestKalaCase_EndToEnd_FractionalFinancials(t *testing.T) {
	sch := caseSchema(t)
	resp := &datasource.ReadResponse{State: tfsdk.State{Schema: sch}}
	(&caseDataSource{client: decimalClient(t)}).Read(context.Background(),
		datasource.ReadRequest{Config: dsConfig(t, sch, map[string]tftypes.Value{
			"case_number":        tftypes.NewValue(tftypes.String, "KA-1"),
			"include_financials": tftypes.NewValue(tftypes.Bool, true),
		})}, resp)

	if resp.Diagnostics.HasError() {
		t.Fatalf("kala_case must read a case with fractional hours or money; got: %v", resp.Diagnostics)
	}
	root := tftypes.NewAttributePath()
	for name, want := range map[string]float64{
		"registered_hours_total": 1.75, "billed_hours": 0.5,
		"cost": 12345.5, "sales": 19999.99, "result": 7654.49,
		"invoiced": 100.25, "uninvoiced": 0.75, "realised": 1.1,
	} {
		assertNumber(t, resp.State, root.WithAttributeName(name), want)
	}
}

// TestTaskResource_StateWrittenAsInt64StillReads covers the one place the type
// change touches PERSISTED state. A kala_task written by a release that declared
// price_fixed Int64 has "price_fixed": 500 in its state file. Int64 and Float64
// are both tftypes.Number, so the raw value is identical and no StateUpgrader is
// needed -- this asserts that rather than relying on it.
func TestTaskResource_StateWrittenAsInt64StillReads(t *testing.T) {
	sch := taskResSchema(t)
	obj, ok := sch.Type().TerraformType(context.Background()).(tftypes.Object)
	if !ok {
		t.Fatal("task resource schema is not an object")
	}
	vals := map[string]tftypes.Value{}
	for name, typ := range obj.AttributeTypes {
		vals[name] = tftypes.NewValue(typ, nil)
	}
	// Exactly as the Int64 schema serialised it: an integral Number.
	vals["price_fixed"] = tftypes.NewValue(tftypes.Number, big.NewFloat(500))
	vals["name"] = tftypes.NewValue(tftypes.String, "Mount gutter")

	state := tfsdk.State{Schema: sch, Raw: tftypes.NewValue(obj, vals)}
	var m taskResourceModel
	if diags := state.Get(context.Background(), &m); diags.HasError() {
		t.Fatalf("state written under the Int64 schema must read under the Float64 one: %v", diags)
	}
	if m.PriceFixed.ValueFloat64() != 500 {
		t.Errorf("price_fixed = %v, want 500", m.PriceFixed.ValueFloat64())
	}
}
