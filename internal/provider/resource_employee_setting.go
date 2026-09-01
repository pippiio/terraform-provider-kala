package provider

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/techchapter/terraform-provider-kala/internal/client"
)

var (
	_ resource.Resource                = &employeeSettingResource{}
	_ resource.ResourceWithConfigure   = &employeeSettingResource{}
	_ resource.ResourceWithImportState = &employeeSettingResource{}
)

// NewEmployeeSettingResource returns the kala_employee_setting resource.
func NewEmployeeSettingResource() resource.Resource {
	return &employeeSettingResource{}
}

type employeeSettingResource struct {
	client client.Client
}

type employeeSettingModel struct {
	ID             types.String `tfsdk:"id"`
	EmployeeNumber types.Int64  `tfsdk:"employee_number"`
	Key            types.String `tfsdk:"key"`
	Value          types.String `tfsdk:"value"`
	FriendlyName   types.String `tfsdk:"friendly_name"`
	Type           types.String `tfsdk:"type"`
	AllowNewKey    types.Bool   `tfsdk:"allow_new_key"`
}

func (r *employeeSettingResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_employee_setting"
}

func (r *employeeSettingResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a single setting on an existing Kala employee.\n\n" +
			"> **Kala cannot delete settings.** `terraform destroy` removes this resource from " +
			"state and warns; the setting remains on the employee. See ADR-001.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Synthetic identifier, `<employee_number>:<key>`.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"employee_number": schema.Int64Attribute{
				Required:            true,
				MarkdownDescription: "Kala employee number. Changing this forces replacement.",
				PlanModifiers:       []planmodifier.Int64{int64planmodifier.RequiresReplace()},
			},
			"key": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Setting key. Changing this forces replacement — and note that the " +
					"old key is **not** removed, because Kala offers no way to delete a setting.",
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"value": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Setting value. This is the only field Kala returns on read, so it is the only one drift can be detected on.",
			},
			"friendly_name": schema.StringAttribute{
				Required: true,
				MarkdownDescription: "Human-readable label. **Write-only:** Kala requires it on every write " +
					"but never returns it from any read endpoint, so Terraform cannot detect drift on it. " +
					"Changing it re-sends the setting but cannot be verified.",
			},
			"type": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				Default:             stringdefault.StaticString("text"),
				MarkdownDescription: "Setting type. **Write-only**, like `friendly_name`. Defaults to `text`.",
			},
			"allow_new_key": schema.BoolAttribute{
				Optional: true,
				Computed: true,
				Default:  booldefault.StaticBool(false),
				MarkdownDescription: "Permit a `key` not already in use anywhere on the account. Defaults to " +
					"`false` because Kala cannot delete settings: a mistyped key stays on the employee's " +
					"record permanently. Set to `true` when deliberately introducing a new setting.",
			},
		},
	}
}

func (r *employeeSettingResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	c, ok := req.ProviderData.(*providerClients)
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected provider data type",
			"The kala_employee_setting resource expected configured Kala clients. This is a bug in the provider.",
		)
		return
	}
	r.client = c.Web
}

// validateKey enforces the unknown-key guard.
//
// Settings cannot be deleted in Kala — only overwritten — so a key created by a
// typo is permanent. The guard blocks by default and suggests the nearest
// existing key, which turns a transposed "default_work_type" from a permanent
// record on a real person into a caught mistake.
func (r *employeeSettingResource) validateKey(ctx context.Context, key string, allowNew bool) (warn string, err error) {
	if allowNew {
		return "", nil
	}

	scan, listErr := r.client.ScanSettingKeys(ctx)
	if listErr != nil {
		return "", fmt.Errorf("could not list existing setting keys to validate %q: %w", key, listErr)
	}
	existing := scan.Keys

	for _, k := range existing {
		if k == key {
			return "", nil
		}
	}

	msg := fmt.Sprintf("The setting key %q is not in use anywhere on this Kala account.\n\n", key)
	if suggestion := client.ClosestKey(key, existing); suggestion != "" {
		msg += fmt.Sprintf("Did you mean %q?\n\n", suggestion)
	}
	msg += "Kala provides no way to delete a setting, so a mistyped key stays on this employee's " +
		"record permanently. If this key is intentional and new, set allow_new_key = true."

	return "", fmt.Errorf("%s", msg)
}

func (r *employeeSettingResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan employeeSettingModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() || !r.requireClient(&resp.Diagnostics) {
		return
	}

	if _, err := r.validateKey(ctx, plan.Key.ValueString(), plan.AllowNewKey.ValueBool()); err != nil {
		resp.Diagnostics.AddAttributeError(path.Root("key"), "Unknown setting key", err.Error())
		return
	}

	if err := r.apply(ctx, plan); err != nil {
		resp.Diagnostics.AddError("Could not write Kala employee setting", err.Error())
		return
	}

	// Read back: the API's {success} body is not proof the value landed.
	if err := r.verify(ctx, plan); err != nil {
		resp.Diagnostics.AddError("Setting write could not be verified", err.Error())
		return
	}

	plan.ID = types.StringValue(settingID(plan.EmployeeNumber.ValueInt64(), plan.Key.ValueString()))
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *employeeSettingResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state employeeSettingModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() || !r.requireClient(&resp.Diagnostics) {
		return
	}

	employee, err := r.client.GetEmployee(ctx, state.EmployeeNumber.ValueInt64())
	if err != nil {
		// A vanished employee is drift, not a failure (TF1.2).
		if isNotFound(err) {
			resp.State.RemoveResource(ctx)
			return
		}
		resp.Diagnostics.AddError("Could not read Kala employee", err.Error())
		return
	}

	found := false
	for _, s := range employee.Settings {
		if s.Key == state.Key.ValueString() {
			state.Value = types.StringValue(s.Value)
			found = true
			break
		}
	}
	if !found {
		// The setting was removed outside Terraform (via the UI, presumably —
		// the API cannot do it). Treat as drift so the next apply re-creates it.
		resp.State.RemoveResource(ctx)
		return
	}

	// friendly_name and type are NOT refreshed. Kala never returns them, so
	// anything we wrote here would be an invention, and comparing against a
	// null would produce a diff on every plan (FR7, risk R1). They are carried
	// forward from prior state untouched.

	state.ID = types.StringValue(settingID(state.EmployeeNumber.ValueInt64(), state.Key.ValueString()))
	resp.Diagnostics.Append(resp.State.Set(ctx, &state)...)
}

func (r *employeeSettingResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan employeeSettingModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() || !r.requireClient(&resp.Diagnostics) {
		return
	}

	// The key is unchanged here — a key change forces replacement — so no
	// unknown-key validation is needed on update.
	if err := r.apply(ctx, plan); err != nil {
		resp.Diagnostics.AddError("Could not update Kala employee setting", err.Error())
		return
	}
	if err := r.verify(ctx, plan); err != nil {
		resp.Diagnostics.AddError("Setting update could not be verified", err.Error())
		return
	}

	plan.ID = types.StringValue(settingID(plan.EmployeeNumber.ValueInt64(), plan.Key.ValueString()))
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

// Delete performs no API call.
//
// Kala has no endpoint to remove a setting (ADR-001, guardrail ARCH1.6). The
// resource leaves state, the setting stays on the employee, and the warning says
// so explicitly rather than letting the operator infer a cleanup that did not
// happen.
func (r *employeeSettingResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state employeeSettingModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	resp.Diagnostics.AddWarning(
		"Setting removed from Terraform state but not from Kala",
		fmt.Sprintf(
			"The setting %q on employee %d has been removed from Terraform state, but it still "+
				"exists in Kala with value %q.\n\n"+
				"The Kala API provides no way to delete a setting — it can only be overwritten. "+
				"To clear it, set value = \"\" and apply before destroying, or remove it through "+
				"the Kala interface.",
			state.Key.ValueString(),
			state.EmployeeNumber.ValueInt64(),
			state.Value.ValueString(),
		),
	)

	tflog.Debug(ctx, "removed employee setting from state without upstream delete", map[string]any{
		"employee_number": state.EmployeeNumber.ValueInt64(),
		"key":             state.Key.ValueString(),
	})
}

// ImportState accepts "<employee_number>:<key>".
func (r *employeeSettingResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	number, key, err := parseSettingID(req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Invalid import ID", err.Error())
		return
	}

	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("employee_number"), number)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("key"), key)...)
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), settingID(number, key))...)

	// friendly_name cannot be recovered — Kala never returns it. The user must
	// supply it in configuration; without it the next plan shows a diff they
	// cannot resolve by reading the remote object.
	resp.Diagnostics.AddWarning(
		"friendly_name could not be imported",
		"Kala does not return friendly_name from any read endpoint, so it cannot be recovered on "+
			"import. Set it in your configuration to match what was originally written, or the next "+
			"apply will overwrite it with whatever your configuration specifies.",
	)
}

// --- helpers -------------------------------------------------------------

func (r *employeeSettingResource) requireClient(diags interface{ AddError(string, string) }) bool {
	if r.client == nil {
		diags.AddError(
			"Kala client not configured",
			"The provider was not configured before this resource was used. This is a bug in the provider.",
		)
		return false
	}
	return true
}

func (r *employeeSettingResource) apply(ctx context.Context, m employeeSettingModel) error {
	return r.client.ApplyEmployeeSetting(ctx,
		m.EmployeeNumber.ValueInt64(),
		client.Setting{Key: m.Key.ValueString(), Value: m.Value.ValueString()},
		client.SettingMetadata{
			FriendlyName: m.FriendlyName.ValueString(),
			Type:         m.Type.ValueString(),
		},
	)
}

// verify re-reads the employee and confirms the written value is present.
//
// HTTP 200 plus {"success":true} is the API's claim; this is the confirmation.
func (r *employeeSettingResource) verify(ctx context.Context, m employeeSettingModel) error {
	employee, err := r.client.GetEmployee(ctx, m.EmployeeNumber.ValueInt64())
	if err != nil {
		return fmt.Errorf("reading back employee %d: %w", m.EmployeeNumber.ValueInt64(), err)
	}

	want := m.Value.ValueString()
	for _, s := range employee.Settings {
		if s.Key == m.Key.ValueString() {
			if s.Value != want {
				return fmt.Errorf("setting %q reads back as %q, expected %q", m.Key.ValueString(), s.Value, want)
			}
			return nil
		}
	}

	return fmt.Errorf("setting %q was not present on employee %d after writing it",
		m.Key.ValueString(), m.EmployeeNumber.ValueInt64())
}

func settingID(number int64, key string) string {
	return strconv.FormatInt(number, 10) + ":" + key
}

func parseSettingID(id string) (int64, string, error) {
	parts := strings.SplitN(id, ":", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return 0, "", fmt.Errorf(
			"import ID %q is malformed; expected \"<employee_number>:<key>\", for example \"4711:default_work_type\"", id)
	}

	number, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		return 0, "", fmt.Errorf("import ID %q has a non-numeric employee number %q", id, parts[0])
	}
	if number == 0 {
		return 0, "", fmt.Errorf("import ID %q has employee number 0, which is never valid", id)
	}

	return number, parts[1], nil
}

// isNotFound reports whether err means the record does not exist.
//
// Uses errors.Is against the client's sentinel rather than matching on message
// text, which would break the moment wording changed.
func isNotFound(err error) bool {
	return errors.Is(err, client.ErrNotFound)
}
