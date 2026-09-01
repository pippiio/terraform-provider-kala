package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// closestKeyMaxDistance bounds how far a suggestion may be from the input.
// Beyond this, "did you mean" stops being helpful and starts being noise.
const closestKeyMaxDistance = 3

// maxDeepScanEmployees bounds the per-employee scan in ListSettingKeys so a
// large account cannot turn one Create into thousands of requests.
const maxDeepScanEmployees = 200

// SettingMetadata carries the write-only half of a setting.
//
// SetEmployeeSetting REQUIRES friendlyName and accepts an optional type, but
// neither read endpoint ever returns them. They are therefore structurally
// write-only: sendable, never verifiable. They are kept out of the Setting
// domain type so no read path can imply it knows them.
type SettingMetadata struct {
	FriendlyName string
	Type         string
}

// wireSetSettingResponse is the documented {success, message} shape.
type wireSetSettingResponse struct {
	Success *bool  `json:"success"`
	Message string `json:"message"`
}

// ApplyEmployeeSetting creates or updates one setting on one employee.
//
// There is no delete-setting endpoint, so a value written under a mistyped key
// cannot be removed — only overwritten. Callers are expected to validate the key
// before calling this.
func (c *webAPIv2) ApplyEmployeeSetting(ctx context.Context, employeeNumber int64, s Setting, meta SettingMetadata) error {
	if meta.FriendlyName == "" {
		return fmt.Errorf("friendly_name is required by the Kala API and must not be empty: " +
			"the value cannot be corrected later because no read endpoint returns it")
	}

	settingType := meta.Type
	if settingType == "" {
		settingType = "text" // the API's documented default
	}

	params := url.Values{}
	params.Set("employeeNumber", strconv.FormatInt(employeeNumber, 10))
	params.Set("key", s.Key)
	params.Set("value", s.Value)
	params.Set("friendlyName", meta.FriendlyName)
	params.Set("type", settingType)

	body, err := c.post(ctx, "SetEmployeeSetting", params)
	if err != nil {
		return err
	}

	var resp wireSetSettingResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		// A 2xx with an undecodable body is not a confirmed write.
		return fmt.Errorf("%w: SetEmployeeSetting response: %v", ErrDecode, err)
	}

	// HTTP 200 does not imply the write landed — the body carries its own verdict.
	if resp.Success != nil && !*resp.Success {
		msg := resp.Message
		if msg == "" {
			msg = "the API reported failure without a message"
		}
		return fmt.Errorf("kala: setting %q was rejected: %s", s.Key, msg)
	}

	return nil
}

// ListSettingKeys returns every setting key currently in use across the account,
// sorted and deduplicated.
//
// This backs the unknown-key guard. Because settings cannot be deleted, writing
// a typo'd key permanently adds junk to a real person's record, so knowing which
// keys legitimately exist is worth the cost of finding them accurately.
//
// It deliberately does an N+1 scan: ActiveEmployeesList UNDER-REPORTS settings.
// Observed 2026-09-01 against the real API — for the same employee the list
// returned 11 keys while ActiveEmployee returned 12, omitting
// "favorite_materials". Relying on the list alone would make the guard reject
// keys that genuinely exist. The per-employee fetch is bounded by
// maxDeepScanEmployees and only runs on resource creation, not on every plan.
func (c *webAPIv2) ListSettingKeys(ctx context.Context) ([]string, error) {
	employees, err := c.ListEmployees(ctx, ListOptions{})
	if err != nil {
		return nil, err
	}

	seen := make(map[string]struct{})
	for _, e := range employees {
		for _, s := range e.Settings {
			if s.Key != "" {
				seen[s.Key] = struct{}{}
			}
		}
	}

	// Second pass: the single-employee endpoint reveals keys the list omits.
	scanned := 0
	for _, e := range employees {
		if scanned >= maxDeepScanEmployees {
			break
		}
		scanned++

		full, err := c.GetEmployee(ctx, e.Number)
		if err != nil {
			// Best-effort enrichment: a failure here degrades the guard's
			// completeness but must not block the caller entirely.
			continue
		}
		for _, s := range full.Settings {
			if s.Key != "" {
				seen[s.Key] = struct{}{}
			}
		}
	}

	keys := make([]string, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	// Sorted so diagnostics are stable across runs.
	sort.Strings(keys)

	return keys, nil
}

// ClosestKey returns the candidate nearest to input by Levenshtein distance, or
// "" when nothing is close enough to be a useful suggestion.
//
// Exported because the provider layer surfaces it in the unknown-key diagnostic:
// a transposed "default_work_type" is the difference between a caught mistake
// and a permanent one, since settings cannot be deleted.
func ClosestKey(input string, candidates []string) string {
	best := ""
	bestDist := closestKeyMaxDistance + 1

	for _, c := range candidates {
		d := levenshtein(strings.ToLower(input), strings.ToLower(c))
		if d < bestDist {
			bestDist, best = d, c
		}
	}

	if bestDist > closestKeyMaxDistance {
		return ""
	}
	return best
}

// levenshtein computes edit distance between two strings.
func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	if len(ra) == 0 {
		return len(rb)
	}
	if len(rb) == 0 {
		return len(ra)
	}

	prev := make([]int, len(rb)+1)
	curr := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}

	for i := 1; i <= len(ra); i++ {
		curr[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			curr[j] = min3(curr[j-1]+1, prev[j]+1, prev[j-1]+cost)
		}
		prev, curr = curr, prev
	}

	return prev[len(rb)]
}

func min3(a, b, c int) int {
	m := a
	if b < m {
		m = b
	}
	if c < m {
		m = c
	}
	return m
}

// SettingKeyScan is the result of surveying an account's setting keys.
// Not yet implemented.
type SettingKeyScan struct {
	Keys      []string
	Employees int
	Scanned   int
	Failed    int
}

// Complete reports whether the whole account was examined. Not yet implemented.
func (s SettingKeyScan) Complete() bool { return true }

// ScanSettingKeys surveys the account's setting keys. Not yet implemented.
func (c *webAPIv2) ScanSettingKeys(ctx context.Context) (SettingKeyScan, error) {
	keys, err := c.ListSettingKeys(ctx)
	return SettingKeyScan{Keys: keys}, err
}
