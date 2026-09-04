package client

import (
	"context"
	"errors"
	"os"
	"testing"
)

// Opt-in check against the real API. Skipped unless KALA_SMOKE=1.
func TestSmoke_RealAPI(t *testing.T) {
	if os.Getenv("KALA_SMOKE") != "1" {
		t.Skip("set KALA_SMOKE=1 to run against the real API")
	}
	key := os.Getenv("KALA_API_KEY")
	if key == "" {
		t.Skip("KALA_API_KEY not set")
	}

	c := New(Config{APIKey: key})
	ctx := context.Background()

	if err := c.Ping(ctx); err != nil {
		t.Fatalf("Ping: %v", err)
	}
	t.Log("Ping OK")

	emps, err := c.ListEmployees(ctx, ListOptions{PageSize: 200})
	if err != nil {
		t.Fatalf("ListEmployees: %v", err)
	}
	t.Logf("ListEmployees returned %d employee(s)", len(emps))

	// The fix under test: an unknown number must be ErrNotFound, not ErrDecode.
	if _, err := c.GetEmployee(ctx, 99999999); !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown employee: want ErrNotFound, got %v", err)
	} else {
		t.Log("unknown employee correctly classified as ErrNotFound")
	}
}
