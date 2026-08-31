package client

import (
	"encoding/json"
	"fmt"
)

// Wire types mirror the webapiv2 JSON exactly and are deliberately unexported:
// nothing outside this package should know the upstream's field names or its
// PascalCase/camelCase quirks (guardrail ARCH1.4, risk R7).
//
// The API documents these field NAMES but not their types, so the types below
// are a hypothesis to be confirmed by the Phase 4 smoke test (risk R1).

type wireEmployee struct {
	// Number is a pointer so that "absent" and "present but 0" are
	// distinguishable. Both are rejected, but only a pointer lets us tell an
	// explicit null from a missing key when diagnosing upstream changes.
	Number   *int64        `json:"number"`
	Name     string        `json:"name"`
	Title    string        `json:"title"`
	Image    string        `json:"image"`
	Phone    string        `json:"phone"`
	IsAdmin  bool          `json:"isAdmin"`
	IsLeader bool          `json:"isLeader"`
	Settings []wireSetting `json:"settings"`
}

type wireSetting struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// toDomain converts a decoded wire record into the provider-owned Employee.
//
// It enforces the one strict rule: a record must have an identity. Everything
// else is lenient, because an undocumented API is likelier to add fields than
// to guarantee the ones it has.
func (w wireEmployee) toDomain() (Employee, error) {
	if w.Number == nil || *w.Number == 0 {
		return Employee{}, fmt.Errorf("%w: employee record has no 'number' identity", ErrDecode)
	}

	// Always non-nil: a nil slice would render as null in Terraform state and
	// produce a spurious diff against a config that declares no settings.
	settings := make([]Setting, 0, len(w.Settings))
	for _, s := range w.Settings {
		// Mapped field-by-field rather than via a Setting(s) conversion.
		// staticcheck suggests the conversion (S1016), but it is only valid
		// while the two structs stay structurally identical, and this is a
		// translation boundary that exists precisely so they can diverge — the
		// internal API's worker settings have a different shape, and reads may
		// gain friendlyName/type upstream.
		settings = append(settings, Setting{ //nolint:staticcheck // S1016: explicit mapping is intentional at the wire/domain boundary
			Key:   s.Key,
			Value: s.Value,
		})
	}

	return Employee{
		Number:   *w.Number,
		Name:     w.Name,
		Title:    w.Title,
		Phone:    w.Phone,
		Image:    w.Image,
		IsAdmin:  w.IsAdmin,
		IsLeader: w.IsLeader,
		Settings: settings,
	}, nil
}

// decodeEmployee decodes a single-employee response body.
func decodeEmployee(body []byte) (Employee, error) {
	var w wireEmployee
	if err := json.Unmarshal(body, &w); err != nil {
		return Employee{}, fmt.Errorf("%w: %v", ErrDecode, err)
	}
	return w.toDomain()
}

// decodeEmployeeList decodes an employee-list response body.
//
// A single invalid record fails the whole page. Dropping it instead would
// silently return fewer employees than exist, which Terraform would read as a
// deletion.
func decodeEmployeeList(body []byte) ([]Employee, error) {
	var ws []wireEmployee
	if err := json.Unmarshal(body, &ws); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrDecode, err)
	}

	out := make([]Employee, 0, len(ws))
	for i, w := range ws {
		e, err := w.toDomain()
		if err != nil {
			return nil, fmt.Errorf("%w (record at index %d)", err, i)
		}
		out = append(out, e)
	}
	return out, nil
}
