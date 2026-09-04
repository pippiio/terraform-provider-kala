package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// Field-level writes on the internal app API.
//
// Kala is inconsistent with itself in two ways that MUST be reproduced exactly
// rather than normalised, because getting either wrong fails silently:
//
//  1. The identifier key is "workerNr" on the Set* endpoints but "workerID" on
//     the Change* endpoints — except ChangeWorkerName, which uses "workerId",
//     and ChangeBoss, which uses "workerNr".
//  2. The Set* paths carry a trailing slash; ChangeWorkerDepartment,
//     ChangeLeaderNote, and ChangeDateOfEmployment do not — but
//     ChangeWorkerName and ChangeBoss do.
//
// In other words there is no rule, only a table. Both properties are captured
// per-endpoint below so no caller has to remember them, and each entry records
// what was observed rather than what the pattern would predict.

// workerFieldSpec describes one field-setting endpoint.
type workerFieldSpec struct {
	// Endpoint path, including or omitting the trailing slash exactly as Kala
	// expects it.
	endpoint string

	// idKey is the JSON key carrying the worker identifier: "workerNr" or
	// "workerID". The VALUE is the same either way — medarbejderNr, workerNr,
	// workerId, and employeeNumber were confirmed identical on 2026-09-01 —
	// but the key is not.
	idKey string

	// valueKey is the JSON key carrying the new value.
	valueKey string

	// readBack returns the field's current value from a WorkerInfo record, so
	// the write can be verified (ARCH1.8).
	readBack func(WorkerInfo) string
}

// WorkerField identifies a settable field on an employee.
type WorkerField string

const (
	FieldPhone        WorkerField = "phone"
	FieldTitle        WorkerField = "title"
	FieldInitials     WorkerField = "initials"
	FieldLicensePlate WorkerField = "license_plate"
	FieldDepartment   WorkerField = "department"
	FieldLeaderNote   WorkerField = "leader_note"
	FieldName         WorkerField = "name"
)

var workerFieldSpecs = map[WorkerField]workerFieldSpec{
	FieldPhone: {
		endpoint: "/api/SetPhone/", idKey: "workerNr", valueKey: "phone",
		readBack: func(w WorkerInfo) string { return w.Phone },
	},
	FieldTitle: {
		endpoint: "/api/SetTitle/", idKey: "workerNr", valueKey: "title",
		readBack: func(w WorkerInfo) string { return w.Title },
	},
	FieldInitials: {
		endpoint: "/api/SetWorkerInitials/", idKey: "workerNr", valueKey: "initials",
		readBack: func(w WorkerInfo) string { return w.Initials },
	},
	FieldLicensePlate: {
		endpoint: "/api/SetLicensePlate/", idKey: "workerNr", valueKey: "licensePlate",
		readBack: func(w WorkerInfo) string { return w.LicensePlate },
	},
	// Note: no trailing slash, and workerID rather than workerNr.
	FieldDepartment: {
		endpoint: "/api/ChangeWorkerDepartment", idKey: "workerID", valueKey: "department",
		readBack: func(w WorkerInfo) string { return w.Department },
	},
	FieldLeaderNote: {
		endpoint: "/api/ChangeLeaderNote", idKey: "workerID", valueKey: "leaderNote",
		readBack: func(w WorkerInfo) string { return w.LeaderNote },
	},
	// Breaks BOTH of the patterns above: a Change* endpoint that keeps the
	// trailing slash and spells the identifier "workerId". Probed 2026-09-04,
	// the endpoint in fact accepted every combination tried — with and without
	// the slash, "workerId" and "workerID" alike — but what is sent here is
	// what Kala's own web client sends, which is the only variant it is safe to
	// assume will keep working.
	FieldName: {
		endpoint: "/api/ChangeWorkerName/", idKey: "workerId", valueKey: "newName",
		readBack: func(w WorkerInfo) string { return w.Name },
	},
}

// WorkerRole identifies a settable boolean role.
type WorkerRole string

const (
	RoleLeader  WorkerRole = "is_leader"
	RoleFinance WorkerRole = "is_finance"
	RolePlanner WorkerRole = "is_planner"
)

type workerRoleSpec struct {
	endpoint string
	valueKey string
	readBack func(WorkerInfo) bool
}

var workerRoleSpecs = map[WorkerRole]workerRoleSpec{
	RoleLeader: {
		endpoint: "/api/SetLeaderRole/", valueKey: "isLeader",
		readBack: func(w WorkerInfo) bool { return w.IsLeader },
	},
	RoleFinance: {
		endpoint: "/api/SetFinanceRole/", valueKey: "isFinance",
		readBack: func(w WorkerInfo) bool { return w.IsFinance },
	},
	RolePlanner: {
		endpoint: "/api/SetPlannerRole/", valueKey: "isPlanner",
		readBack: func(w WorkerInfo) bool { return w.IsPlanner },
	},
}

// SetWorkerField sets one string-valued field and verifies it by read-back.
func (c *internalAPI) SetWorkerField(ctx context.Context, workerNr int64, field WorkerField, value string) error {
	spec, ok := workerFieldSpecs[field]
	if !ok {
		return fmt.Errorf("kala: unknown worker field %q", field)
	}

	payload := map[string]any{
		spec.idKey:    workerNr,
		spec.valueKey: value,
	}
	if err := c.postJSON(ctx, spec.endpoint, payload); err != nil {
		return fmt.Errorf("kala: setting %s for worker %d: %w", field, workerNr, err)
	}

	info, err := c.GetWorkerInfo(ctx, workerNr)
	if err != nil {
		return fmt.Errorf("kala: could not verify %s for worker %d: %w", field, workerNr, err)
	}
	if got := spec.readBack(info); got != value {
		return fmt.Errorf(
			"kala: setting %s for worker %d reported success but reads back as %q, expected %q",
			field, workerNr, got, value)
	}
	return nil
}

// SetWorkerRole sets one boolean role and verifies it by read-back.
func (c *internalAPI) SetWorkerRole(ctx context.Context, workerNr int64, role WorkerRole, value bool) error {
	spec, ok := workerRoleSpecs[role]
	if !ok {
		return fmt.Errorf("kala: unknown worker role %q", role)
	}

	payload := map[string]any{
		"workerNr":    workerNr,
		spec.valueKey: value,
	}
	if err := c.postJSON(ctx, spec.endpoint, payload); err != nil {
		return fmt.Errorf("kala: setting %s for worker %d: %w", role, workerNr, err)
	}

	info, err := c.GetWorkerInfo(ctx, workerNr)
	if err != nil {
		return fmt.Errorf("kala: could not verify %s for worker %d: %w", role, workerNr, err)
	}
	if got := spec.readBack(info); got != value {
		return fmt.Errorf(
			"kala: setting %s for worker %d reported success but reads back as %t, expected %t",
			role, workerNr, got, value)
	}
	return nil
}

// SetWorkerDateOfEmployment sets the employment start date from a plain
// YYYY-MM-DD date.
//
// The endpoint's write format and Kala's read format differ, and naively
// passing the documented shape through causes a perpetual diff. Sending
// 2026-08-31T22:00:00.000Z with gmtOffset 2 reads back as "2026-09-01" — the
// offset shifts the stored date across midnight. Verified 2026-09-01:
// sending midnight UTC with gmtOffset 0 round-trips exactly, so that is what
// this sends regardless of the caller's timezone.
func (c *internalAPI) SetWorkerDateOfEmployment(ctx context.Context, workerNr int64, date string) error {
	date = strings.TrimSpace(date)
	if !isISODate(date) {
		return fmt.Errorf("date of employment must be a plain date in YYYY-MM-DD form, got %q", date)
	}

	payload := map[string]any{
		"workerID":            workerNr, // note: workerID, not workerNr
		"newDateOfEmployment": date + "T00:00:00.000Z",
		"gmtOffset":           0, // midnight UTC at offset 0 cannot shift the date
	}
	if err := c.postJSON(ctx, "/api/ChangeDateOfEmployment", payload); err != nil {
		return fmt.Errorf("kala: setting date of employment for worker %d: %w", workerNr, err)
	}

	info, err := c.GetWorkerInfo(ctx, workerNr)
	if err != nil {
		return fmt.Errorf("kala: could not verify date of employment for worker %d: %w", workerNr, err)
	}
	if info.DateOfEmployment != date {
		return fmt.Errorf(
			"kala: setting date of employment for worker %d reported success but reads back as %q, expected %q",
			workerNr, info.DateOfEmployment, date)
	}
	return nil
}

// isISODate reports whether s is exactly YYYY-MM-DD and a real calendar date.
func isISODate(s string) bool {
	if len(s) != 10 {
		return false
	}
	_, err := time.Parse("2006-01-02", s)
	return err == nil
}

// postJSON sends an authenticated JSON POST to the internal API.
// SetWorkerBoss sets which employee an employee reports to, and VERIFIES the
// result by reading it back (ARCH1.8).
//
// Kala calls this the "first boss". It is the one worker field that is neither
// a string nor a role flag, which is why it is not in the table above:
// ChangeBoss takes an employee number and reads back as a nested object.
//
// It is also a third spelling of the identifier — "workerNr", where the other
// Change* endpoints use "workerID". Observed 2026-09-04.
func (c *internalAPI) SetWorkerBoss(ctx context.Context, workerNr, bossNr int64) error {
	if bossNr == workerNr {
		// Kala's own interface cannot express this, and the read-back would
		// look like a success. Refusing here is cheaper than a record that
		// reports to itself.
		return fmt.Errorf("kala: employee %d cannot be their own boss", workerNr)
	}

	payload := map[string]any{"workerNr": workerNr, "bossNr": bossNr}
	if err := c.postJSON(ctx, "/api/ChangeBoss/", payload); err != nil {
		return fmt.Errorf("kala: setting the boss of worker %d to %d: %w", workerNr, bossNr, err)
	}

	info, err := c.GetWorkerInfo(ctx, workerNr)
	if err != nil {
		return fmt.Errorf("kala: could not verify the boss of worker %d: %w", workerNr, err)
	}
	if info.BossNumber != bossNr {
		return fmt.Errorf(
			"kala: setting the boss of worker %d reported success but reads back as %d, expected %d",
			workerNr, info.BossNumber, bossNr)
	}
	return nil
}

func (c *internalAPI) postJSON(ctx context.Context, endpoint string, payload map[string]any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("building request for %s: %w", endpoint, err)
	}

	_, err = c.authedRequest(ctx, http.MethodPost, endpoint, body, contentTypeHeader)
	return err
}
