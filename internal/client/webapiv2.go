package client

import (
	"context"
	"time"
)

// Stubs — implemented in the GREEN step (Task 2.7).

type Config struct {
	Endpoint     string
	APIKey       string
	Company      int64
	Timeout      time.Duration
	MaxRetries   int
	retryBaseDur time.Duration
}

type webAPIv2 struct{ cfg Config }

func newWebAPIv2(cfg Config) *webAPIv2 { return &webAPIv2{cfg: cfg} }

func (c *webAPIv2) Ping(_ context.Context) error { return nil }

func (c *webAPIv2) ListEmployees(_ context.Context, _ ListOptions) ([]Employee, error) {
	return nil, nil
}

func (c *webAPIv2) GetEmployee(_ context.Context, _ int64) (Employee, error) {
	return Employee{}, nil
}
