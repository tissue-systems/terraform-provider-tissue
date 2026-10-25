package provider

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

func TestChannelNameProblem(t *testing.T) {
	ok := []string{"deploys", "db-1", "nightly backup", "café", "2fa.codes", "a_b"}
	for _, name := range ok {
		if p := channelNameProblem(name); p != "" {
			t.Errorf("%q: %s", name, p)
		}
	}
	bad := map[string]string{
		"":                      "empty",
		"Deploys":               "lowercase",
		" deploys":              "start with a space",
		"deploys ":              "end with a space",
		"two  spaces":           "two spaces",
		"-dash":                 "starts with a letter",
		"slash/no":              "can't be used",
		"cafe\u0301":            "NFC",
		strings.Repeat("x", 33): "at most 32",
	}
	for name, want := range bad {
		p := channelNameProblem(name)
		if !strings.Contains(p, want) {
			t.Errorf("%q: got %q, want it to mention %q", name, p, want)
		}
	}
}

func TestOverrideBounds(t *testing.T) {
	start, ok := overrideStart("2026-12-24")
	if !ok || start.Format("2006-01-02T15:04") != "2026-12-24T00:00" {
		t.Fatalf("date start: %v %v", start, ok)
	}
	end, ok := overrideEnd("2026-12-24")
	if !ok || end.Format("2006-01-02T15:04") != "2026-12-25T00:00" {
		t.Fatalf("date end covers the whole day: %v %v", end, ok)
	}
	if _, ok := overrideStart("2026-12-24T09:30"); !ok {
		t.Error("local time refused")
	}
	for _, s := range []string{"2026-12-24 09:30", "2026-12-24T09:30:00", "24/12/2026", "2026-12-24T9:30"} {
		if _, ok := overrideStart(s); ok {
			t.Errorf("%q accepted", s)
		}
	}
}

func TestScopeError(t *testing.T) {
	f := newFakeMast(t)
	c := NewClient(f.srv.URL, readToken, "test")

	_, err := c.CreateChannel(context.Background(), map[string]any{"name": "x", "kind": "channel"})
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 403 {
		t.Fatalf("want a 403, got %v", err)
	}
	if !strings.Contains(err.Error(), "needs the mast:write scope") {
		t.Errorf("the error doesn't name the scope: %s", err)
	}

	if _, err := c.Channels(context.Background()); err != nil {
		t.Errorf("a read with mast:read failed: %v", err)
	}
}

func TestRoleCeilingErrorKeepsServerMessage(t *testing.T) {
	err := &APIError{Method: "POST", Path: "/v1/mast/channels", Status: 403, Code: "forbidden",
		Message: "Role 'developer' cannot use scope 'mast:write'."}
	got := err.Error()
	if !strings.Contains(got, "Role 'developer'") || strings.Contains(got, "make a token") {
		t.Errorf("got %q", got)
	}
}

func TestNotFound(t *testing.T) {
	f := newFakeMast(t)
	c := NewClient(f.srv.URL, fullToken, "test")
	_, err := c.GetSchedule(context.Background(), "sc_missing")
	if !isNotFound(err) {
		t.Fatalf("want a 404, got %v", err)
	}
}

func TestFindSchedule(t *testing.T) {
	f := newFakeMast(t)
	c := NewClient(f.srv.URL, fullToken, "test")
	ctx := context.Background()
	a, err := c.CreateSchedule(ctx, map[string]any{"name": "primary"})
	if err != nil {
		t.Fatal(err)
	}
	if got, err := c.FindSchedule(ctx, "primary"); err != nil || got.ID != a.ID {
		t.Fatalf("by name: %v %v", got, err)
	}
	if got, err := c.FindSchedule(ctx, a.ID); err != nil || got.Name != "primary" {
		t.Fatalf("by id: %v %v", got, err)
	}
	b, _ := c.CreateSchedule(ctx, map[string]any{"name": "primary"})
	_, err = c.FindSchedule(ctx, "primary")
	if err == nil || !strings.Contains(err.Error(), a.ID) || !strings.Contains(err.Error(), b.ID) {
		t.Fatalf("an ambiguous name should list both ids, got %v", err)
	}
}

// Refreshing N channels needs the channel list and the policy list N times;
// the client reads each once until something writes.
func TestListsReadOnceUntilAWrite(t *testing.T) {
	f := newFakeMast(t)
	c := NewClient(f.srv.URL, fullToken, "test")
	ctx := context.Background()
	var ids []string
	for _, name := range []string{"a", "b", "c"} {
		ch, err := c.CreateChannel(ctx, map[string]any{"name": name, "kind": "channel"})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, ch.ID)
	}
	for _, id := range ids {
		if _, err := c.Channels(ctx); err != nil {
			t.Fatal(err)
		}
		if _, err := c.PolicyOf(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	if n := f.count("GET /v1/mast/state"); n != 1 {
		t.Errorf("%d state reads for 3 channels, want 1", n)
	}
	if n := f.count("GET /v1/mast/policies"); n != 1 {
		t.Errorf("%d policy list reads for 3 channels, want 1", n)
	}

	po, err := c.CreatePolicy(ctx, map[string]any{"name": "p", "steps": []map[string]any{
		{"after_secs": 0, "target": map[string]string{"type": "everyone"}, "priority": "page"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.UpdateChannel(ctx, ids[0], map[string]any{"policy_id": po.ID, "name": "renamed"}); err != nil {
		t.Fatal(err)
	}
	if got, err := c.PolicyOf(ctx, ids[0]); err != nil || got != po.ID {
		t.Errorf("policy after the write: %q %v, want %s", got, err, po.ID)
	}
	list, err := c.Channels(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.ContainsFunc(list, func(ch apiChannel) bool { return ch.Name == "renamed" }) {
		t.Error("the channel list wasn't read again after the rename")
	}
}

func TestZoneProblem(t *testing.T) {
	for _, tz := range []string{"", "UTC", "utc", "Z", "Europe/Berlin", "America/Los_Angeles", "+0530", "-08:00", "+14:00"} {
		if p := zoneProblem(tz); p != "" {
			t.Errorf("%q: %s", tz, p)
		}
	}
	for _, tz := range []string{"Europe/Berln", "Europe/Berlin!", "+53", "+2500", "+0999", "-1500", "local", "Local"} {
		if zoneProblem(tz) == "" {
			t.Errorf("%q accepted", tz)
		}
	}
}

func TestNilClientIsAnError(t *testing.T) {
	var c *Client
	if _, err := c.Channels(context.Background()); err == nil || !strings.Contains(err.Error(), "isn't configured yet") {
		t.Fatalf("got %v", err)
	}
	c.lockRotation("sc_1")()
}
