package client

import (
	"context"
	"os"
	"strconv"
	"testing"
)

// Integration probes against a REAL Kala tenant. Every one is skipped unless
// explicitly enabled, so `go test ./...` and CI stay hermetic.
//
//	KALA_PROBE=1       read-only
//	KALA_CREATE=1      CREATES A REAL EMPLOYEE — irreversible, Kala has no delete
//	KALA_DEACTIVATE=1  deactivates a worker
//
// Credentials come from KALA_API_KEY / KALA_USERNAME / KALA_PASSWORD.

func internalFromEnv() InternalClient {
	return NewInternal(InternalConfig{
		Username: os.Getenv("KALA_USERNAME"),
		Password: os.Getenv("KALA_PASSWORD"),
	})
}

func TestIntegration_InternalRead(t *testing.T) {
	if os.Getenv("KALA_PROBE") != "1" {
		t.Skip("set KALA_PROBE=1")
	}

	workers, err := internalFromEnv().ListWorkers(context.Background())
	if err != nil {
		t.Fatalf("ListWorkers: %v", err)
	}

	t.Logf("handshake OK; %d worker(s)", len(workers))
	for _, w := range workers {
		// Identifiers and state only — never names or phone numbers.
		t.Logf("  workerNr=%d workerId=%d isValidated=%t", w.WorkerNr, w.WorkerID, w.IsValidated)
	}
}

// Confirmed 2026-09-01 against the live tenant: creating with medarbejderNr=3
// produced workerNr=3, workerId=3, and webapiv2 employeeNumber=3. The three
// identifiers are the same value.
func TestIntegration_CreateEmployee(t *testing.T) {
	if os.Getenv("KALA_CREATE") != "1" {
		t.Skip("set KALA_CREATE=1 — CREATES A REAL EMPLOYEE that cannot be deleted")
	}

	num, err := strconv.ParseInt(os.Getenv("KALA_CREATE_NUMBER"), 10, 64)
	if err != nil {
		t.Fatalf("KALA_CREATE_NUMBER: %v", err)
	}

	internal := internalFromEnv()
	ctx := context.Background()

	existing, err := internal.ListWorkers(ctx)
	if err != nil {
		t.Fatalf("ListWorkers: %v", err)
	}
	for _, w := range existing {
		if w.WorkerNr == num {
			t.Fatalf("workerNr %d already exists — refusing to collide", num)
		}
	}

	created, err := internal.CreateWorker(ctx, NewWorker{
		Number: num,
		Email:  os.Getenv("KALA_CREATE_EMAIL"),
		Name:   os.Getenv("KALA_CREATE_NAME"),
	})
	if err != nil {
		t.Fatalf("CreateWorker: %v", err)
	}
	t.Logf("created workerNr=%d workerId=%d isValidated=%t", created.WorkerNr, created.WorkerID, created.IsValidated)

	// The identifier experiment.
	emp, err := New(Config{APIKey: os.Getenv("KALA_API_KEY")}).GetEmployee(ctx, num)
	if err != nil {
		t.Fatalf("webapiv2 does not see employeeNumber=%d: %v", num, err)
	}
	if emp.Number != created.WorkerNr {
		t.Fatalf("IDENTIFIER MISMATCH: workerNr=%d but employeeNumber=%d", created.WorkerNr, emp.Number)
	}
	t.Logf("identifiers agree: medarbejderNr = workerNr = employeeNumber = %d", emp.Number)
}

func TestIntegration_Deactivate(t *testing.T) {
	if os.Getenv("KALA_DEACTIVATE") != "1" {
		t.Skip("set KALA_DEACTIVATE=1")
	}
	num, err := strconv.ParseInt(os.Getenv("KALA_CREATE_NUMBER"), 10, 64)
	if err != nil {
		t.Fatalf("KALA_CREATE_NUMBER: %v", err)
	}

	c := internalFromEnv()
	ctx := context.Background()

	if err := c.SetWorkerValidated(ctx, num, false); err != nil {
		t.Fatalf("SetWorkerValidated: %v", err)
	}

	after, err := c.GetWorker(ctx, num)
	if err != nil {
		t.Fatalf("GetWorker after: %v", err)
	}
	if after.IsValidated {
		t.Fatal("worker still active after deactivation")
	}
	// Confirmed: a deactivated worker REMAINS in /api/Workers with
	// isValidated=false rather than disappearing, so drift stays detectable.
	t.Logf("workerNr=%d isValidated=%t, still listed", after.WorkerNr, after.IsValidated)
}
