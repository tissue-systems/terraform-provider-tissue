package provider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"
)

// Client talks to the Mast part of the Tissue management API.
type Client struct {
	server    string
	token     string
	userAgent string
	http      *http.Client

	// The server saves a schedule by reading the row and writing all of it
	// back, so a rotation edit and an override edit on the same rotation must
	// not interleave.
	rotationLocks sync.Map

	// Every channel and vital refreshes from the whole channel list and the
	// whole policy list. Each is read once and kept until the next write.
	channels cached[[]apiChannel]
	policies cached[[]apiPolicy]
}

type cached[T any] struct {
	mu    sync.Mutex
	value T
	ok    bool
}

// get holds the lock across fetch, so callers asking at once share one read,
// and a drop that comes in during the read waits and then discards it.
func (c *cached[T]) get(fetch func() (T, error)) (T, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.ok {
		return c.value, nil
	}
	v, err := fetch()
	if err == nil {
		c.value, c.ok = v, true
	}
	return v, err
}

func (c *cached[T]) drop() {
	c.mu.Lock()
	defer c.mu.Unlock()
	var zero T
	c.value, c.ok = zero, false
}

func NewClient(server, token, userAgent string) *Client {
	return &Client{
		server:    strings.TrimRight(server, "/"),
		token:     token,
		userAgent: userAgent,
		http:      &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *Client) lockRotation(id string) func() {
	if c == nil {
		return func() {}
	}
	mu, _ := c.rotationLocks.LoadOrStore(id, &sync.Mutex{})
	mu.(*sync.Mutex).Lock()
	return mu.(*sync.Mutex).Unlock
}

// APIError is a non-2xx answer from the API.
type APIError struct {
	Method  string
	Path    string
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string {
	msg := e.Message
	if msg == "" {
		msg = http.StatusText(e.Status)
	}
	out := fmt.Sprintf("%s %s: %d %s", e.Method, e.Path, e.Status, msg)
	if e.Status == http.StatusForbidden && (e.Code == "invalid_scope" || e.Code == "") {
		out += fmt.Sprintf(" This call needs the %s scope; make a token with mast:read and mast:write.", scopeFor(e.Method))
	}
	return out
}

// The API checks mast:read on reads and mast:write on everything else.
func scopeFor(method string) string {
	if method == http.MethodGet {
		return "mast:read"
	}
	return "mast:write"
}

func isNotFound(err error) bool {
	var apiErr *APIError
	return errors.As(err, &apiErr) && apiErr.Status == http.StatusNotFound
}

// errUnconfigured is what every call returns when Configure had no token or
// server to work with because the configuration feeds them from values that
// are only known after apply.
var errUnconfigured = errors.New("the provider isn't configured yet: token or server depends on a value " +
	"that is only known after apply. Set them from values known at plan time, or apply what they come " +
	"from first with -target")

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	if c == nil {
		return errUnconfigured
	}
	if method != http.MethodGet {
		// Dropped after the write, even a failed one: a write the server
		// half applied must not be hidden behind the old lists.
		defer c.channels.drop()
		defer c.policies.drop()
	}
	var reader io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.server+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return err
	}

	if resp.StatusCode >= 300 {
		apiErr := &APIError{Method: method, Path: path, Status: resp.StatusCode}
		var envelope struct {
			Error struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal(data, &envelope) == nil {
			apiErr.Code = envelope.Error.Code
			apiErr.Message = envelope.Error.Message
		}
		if apiErr.Message == "" {
			apiErr.Message = strings.TrimSpace(string(data))
		}
		return apiErr
	}
	if out == nil || len(data) == 0 {
		return nil
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("%s %s: reading the response: %w", method, path, err)
	}
	return nil
}

type apiChannel struct {
	ID                string  `json:"id"`
	Name              string  `json:"name"`
	Kind              string  `json:"kind"`
	CreatedAt         string  `json:"created_at"`
	ArchivedAt        *string `json:"archived_at"`
	URL               *string `json:"url"`
	DefaultPriority   string  `json:"default_priority"`
	MaxPriority       string  `json:"max_priority"`
	Sound             *string `json:"sound"`
	QuietStart        int64   `json:"quiet_start"`
	QuietEnd          int64   `json:"quiet_end"`
	QuietTZ           *string `json:"quiet_tz"`
	DedupeWindowSecs  int64   `json:"dedupe_window_secs"`
	StormThreshold    int64   `json:"storm_threshold"`
	StormWindowSecs   int64   `json:"storm_window_secs"`
	RepeatSecs        int64   `json:"repeat_secs"`
	ExpireSecs        int64   `json:"expire_secs"`
	EscalateAfterSecs int64   `json:"escalate_after_secs"`
	AckTimeoutMin     int64   `json:"ack_timeout_min"`
	CanaryURL         *string `json:"canary_url"`

	PeriodSecs      int64   `json:"period_secs"`
	GraceSecs       int64   `json:"grace_secs"`
	Schedule        *string `json:"schedule"`
	ScheduleTZ      *string `json:"schedule_tz"`
	MaxDurationSecs int64   `json:"max_duration_secs"`
	VitalState      string  `json:"vital_state"`
	VitalPriority   string  `json:"vital_priority"`
	FailThreshold   int64   `json:"fail_threshold"`
	SmartEarlyWarn  bool    `json:"smart_early_warn"`
	Host            *string `json:"host"`
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// GetChannel takes an id or a name. The answer carries no ingest URL.
func (c *Client) GetChannel(ctx context.Context, idOrName string) (*apiChannel, error) {
	var ch apiChannel
	err := c.do(ctx, http.MethodGet, "/v1/mast/channels/"+url.PathEscape(idOrName), nil, &ch)
	return &ch, err
}

// Channels lists the live channels and vitals, keys and URLs included. There
// is no list route of its own: the app's sync payload is the list.
func (c *Client) Channels(ctx context.Context) ([]apiChannel, error) {
	if c == nil {
		return nil, errUnconfigured
	}
	list, err := c.channels.get(func() ([]apiChannel, error) {
		var state struct {
			Channels []apiChannel `json:"channels"`
		}
		err := c.do(ctx, http.MethodGet, "/v1/mast/state", nil, &state)
		return state.Channels, err
	})
	return slices.Clone(list), err
}

func (c *Client) CreateChannel(ctx context.Context, body map[string]any) (*apiChannel, error) {
	var ch apiChannel
	err := c.do(ctx, http.MethodPost, "/v1/mast/channels", body, &ch)
	return &ch, err
}

func (c *Client) UpdateChannel(ctx context.Context, id string, body map[string]any) (*apiChannel, error) {
	var ch apiChannel
	err := c.do(ctx, http.MethodPatch, "/v1/mast/channels/"+url.PathEscape(id), body, &ch)
	return &ch, err
}

func (c *Client) DeleteChannel(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/v1/mast/channels/"+url.PathEscape(id), nil, nil)
}

type apiOverride struct {
	ID           string `json:"id,omitempty"`
	UserID       string `json:"user_id"`
	CoversUserID string `json:"covers_user_id,omitempty"`
	Start        string `json:"start"`
	End          string `json:"end"`
}

type apiSchedule struct {
	ID          string        `json:"id"`
	Name        string        `json:"name"`
	Handoff     string        `json:"handoff"`
	HandoffDay  int64         `json:"handoff_day"`
	HandoffTime string        `json:"handoff_time"`
	LengthHours int64         `json:"length_hours"`
	TZ          string        `json:"tz"`
	Order       []string      `json:"order"`
	Overrides   []apiOverride `json:"overrides"`
	CreatedAt   string        `json:"created_at"`
	Now         *struct {
		UserID string `json:"user_id"`
		Until  string `json:"until"`
	} `json:"now"`
}

func (c *Client) Schedules(ctx context.Context) ([]apiSchedule, error) {
	var list struct {
		Items []apiSchedule `json:"items"`
	}
	err := c.do(ctx, http.MethodGet, "/v1/mast/schedules", nil, &list)
	return list.Items, err
}

func (c *Client) GetSchedule(ctx context.Context, id string) (*apiSchedule, error) {
	var sc apiSchedule
	err := c.do(ctx, http.MethodGet, "/v1/mast/schedules/"+url.PathEscape(id), nil, &sc)
	return &sc, err
}

func (c *Client) CreateSchedule(ctx context.Context, body map[string]any) (*apiSchedule, error) {
	var sc apiSchedule
	err := c.do(ctx, http.MethodPost, "/v1/mast/schedules", body, &sc)
	return &sc, err
}

func (c *Client) UpdateSchedule(ctx context.Context, id string, body map[string]any) (*apiSchedule, error) {
	var sc apiSchedule
	err := c.do(ctx, http.MethodPatch, "/v1/mast/schedules/"+url.PathEscape(id), body, &sc)
	return &sc, err
}

func (c *Client) DeleteSchedule(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/v1/mast/schedules/"+url.PathEscape(id), nil, nil)
}

// FindSchedule resolves an import argument: a schedule id, or a name that
// exactly one schedule has.
func (c *Client) FindSchedule(ctx context.Context, idOrName string) (*apiSchedule, error) {
	if strings.HasPrefix(idOrName, "sc_") {
		return c.GetSchedule(ctx, idOrName)
	}
	list, err := c.Schedules(ctx)
	if err != nil {
		return nil, err
	}
	var found []apiSchedule
	for _, sc := range list {
		if sc.Name == idOrName {
			found = append(found, sc)
		}
	}
	switch len(found) {
	case 0:
		return nil, fmt.Errorf("no rotation named %q", idOrName)
	case 1:
		return &found[0], nil
	}
	ids := make([]string, len(found))
	for i, sc := range found {
		ids[i] = sc.ID
	}
	return nil, fmt.Errorf("%d rotations are named %q (%s); use the id", len(found), idOrName, strings.Join(ids, ", "))
}

type apiTarget struct {
	Type string  `json:"type"`
	ID   *string `json:"id,omitempty"`
}

type apiStep struct {
	AfterSecs int64     `json:"after_secs"`
	Target    apiTarget `json:"target"`
	Priority  string    `json:"priority"`
}

type apiPolicy struct {
	ID              string    `json:"id"`
	Name            string    `json:"name"`
	Steps           []apiStep `json:"steps"`
	RepeatEverySecs *int64    `json:"repeat_every_secs"`
	RepeatCount     *int64    `json:"repeat_count"`
	CreatedAt       string    `json:"created_at"`
	UsedBy          struct {
		Channels []string `json:"channels"`
	} `json:"used_by"`
}

func (c *Client) Policies(ctx context.Context) ([]apiPolicy, error) {
	if c == nil {
		return nil, errUnconfigured
	}
	list, err := c.policies.get(func() ([]apiPolicy, error) {
		var list struct {
			Items []apiPolicy `json:"items"`
		}
		err := c.do(ctx, http.MethodGet, "/v1/mast/policies", nil, &list)
		return list.Items, err
	})
	return slices.Clone(list), err
}

func (c *Client) GetPolicy(ctx context.Context, id string) (*apiPolicy, error) {
	var po apiPolicy
	err := c.do(ctx, http.MethodGet, "/v1/mast/policies/"+url.PathEscape(id), nil, &po)
	return &po, err
}

func (c *Client) CreatePolicy(ctx context.Context, body map[string]any) (*apiPolicy, error) {
	var po apiPolicy
	err := c.do(ctx, http.MethodPost, "/v1/mast/policies", body, &po)
	return &po, err
}

func (c *Client) UpdatePolicy(ctx context.Context, id string, body map[string]any) (*apiPolicy, error) {
	var po apiPolicy
	err := c.do(ctx, http.MethodPatch, "/v1/mast/policies/"+url.PathEscape(id), body, &po)
	return &po, err
}

func (c *Client) DeletePolicy(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/v1/mast/policies/"+url.PathEscape(id), nil, nil)
}

// PolicyOf returns the id of the policy a channel uses, or "". The channel
// row doesn't say; each policy lists the channels pointing at it.
func (c *Client) PolicyOf(ctx context.Context, channelID string) (string, error) {
	list, err := c.Policies(ctx)
	if err != nil {
		return "", err
	}
	for _, po := range list {
		for _, id := range po.UsedBy.Channels {
			if id == channelID {
				return po.ID, nil
			}
		}
	}
	return "", nil
}

type apiPerson struct {
	UserID string `json:"user_id"`
	Email  string `json:"email"`
	Role   string `json:"role"`
	Phones int64  `json:"phones"`
}

func (c *Client) People(ctx context.Context) ([]apiPerson, error) {
	var list struct {
		Items []apiPerson `json:"items"`
	}
	err := c.do(ctx, http.MethodGet, "/v1/mast/responders", nil, &list)
	return list.Items, err
}
