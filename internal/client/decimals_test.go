package client

import (
	"context"
	"testing"
)

// Kala returns DECIMALS for hours and money. registeredHoursTotal came back as
// 0.25 on 2026-09-25, and the client -- which declared every numeric as Go int
// -- failed the whole read with a decode error rather than rounding. It was
// found by accident, by a probe looking for something else, because no fixture
// in this package had ever carried a decimal.
//
// These tests carry decimals in every hour and money field on both read paths.
// Comparisons go through float64(...) deliberately, so each test compiles and
// fails on an assertion whether the field is declared int or float64.

func TestGetCase_DecodesFractionalHoursAndMoney(t *testing.T) {
	m := newCaseAccessMock(t)
	d := accessDetail(2, false, 1, accessItem(7, 3))
	d["registeredHoursTotal"] = 1.75
	d["billedHours"] = 0.5
	d["cost"] = 12345.5
	d["sales"] = 19999.99
	d["result"] = 7654.49
	d["invoiced"] = 100.25
	d["uninvoiced"] = 0.75
	d["realised"] = 1.1
	m.detail = d

	got, err := m.client().GetCase(context.Background(), "KA-1")
	if err != nil {
		t.Fatalf("a case with fractional hours or money must be readable, got: %v", err)
	}

	for _, tc := range []struct {
		name string
		got  float64
		want float64
	}{
		{"RegisteredHoursTotal", float64(got.RegisteredHoursTotal), 1.75},
		{"BilledHours", float64(got.BilledHours), 0.5},
		{"Cost", float64(got.Cost), 12345.5},
		{"Sales", float64(got.Sales), 19999.99},
		{"Result", float64(got.Result), 7654.49},
		{"Invoiced", float64(got.Invoiced), 100.25},
		{"Uninvoiced", float64(got.Uninvoiced), 0.75},
		{"Realised", float64(got.Realised), 1.1},
	} {
		if tc.got != tc.want {
			t.Errorf("%s = %v, want %v — a truncated value is worse than an error", tc.name, tc.got, tc.want)
		}
	}
}

func TestListTasks_DecodesFractionalHoursAndPrice(t *testing.T) {
	m := newTaskMock(t)
	rec := taskRec(5, "Gutter", nil, false)
	rec["registeredHoursTotal"] = 0.25
	rec["billedHours"] = 1.5
	rec["priceFixed"] = 549.95
	m.items = []map[string]any{rec}
	m.total = intp(1)

	scan, err := m.client().ListTasks(context.Background(), TaskQuery{CaseID: 2})
	if err != nil {
		t.Fatalf("a task list with fractional hours or price must be readable, got: %v", err)
	}
	if len(scan.Tasks) != 1 {
		t.Fatalf("got %d tasks, want 1", len(scan.Tasks))
	}
	k := scan.Tasks[0]
	if float64(k.RegisteredHoursTotal) != 0.25 {
		t.Errorf("RegisteredHoursTotal = %v, want 0.25", k.RegisteredHoursTotal)
	}
	if float64(k.BilledHours) != 1.5 {
		t.Errorf("BilledHours = %v, want 1.5", k.BilledHours)
	}
	if k.PriceFixed == nil || float64(*k.PriceFixed) != 549.95 {
		t.Errorf("PriceFixed = %v, want 549.95", k.PriceFixed)
	}
}
