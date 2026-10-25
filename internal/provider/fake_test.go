package provider

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// fakeMast stands in for the Mast routes of the management API. It keeps
// the behaviour the provider has to cope with: defaults filled in, fields
// trimmed or canonicalised, empty strings rendered as null, patch-only
// fields silently ignored on create, max_priority refused from a token,
// overrides replaced wholesale, and a policy's channels known only from
// the policy side.
type fakeMast struct {
	t      *testing.T
	srv    *httptest.Server
	mu     sync.Mutex
	tokens map[string][]string

	channels  map[string]*fakeChannel
	schedules map[string]*apiSchedule
	policies  map[string]*fakePolicy
	people    []apiPerson
	order     []string // creation order of channels, for /state

	requests []string

	// schedulePatchGap holds a schedule PATCH between reading the row and
	// writing all of it back, the window in which the real server can lose a
	// concurrent write to the same rotation.
	schedulePatchGap time.Duration
}

type fakeChannel struct {
	apiChannel
	key      string
	archived bool
	policy   string
}

type fakePolicy struct {
	ID              string
	Name            string
	Steps           []apiStep
	RepeatEverySecs int64
	RepeatCount     int64
	CreatedAt       string
}

const (
	fullToken = "tok_" + "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	readToken = "tok_" + "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

func newFakeMast(t *testing.T) *fakeMast {
	f := &fakeMast{
		t: t,
		tokens: map[string][]string{
			fullToken: {"mast:read", "mast:write"},
			readToken: {"mast:read"},
		},
		channels:  map[string]*fakeChannel{},
		schedules: map[string]*apiSchedule{},
		policies:  map[string]*fakePolicy{},
		people: []apiPerson{
			{UserID: "usr_ana", Email: "ana@example.com", Role: "owner", Phones: 2},
			{UserID: "usr_ben", Email: "Ben@Example.com", Role: "developer", Phones: 1},
			{UserID: "usr_cy", Email: "cy@example.com", Role: "admin", Phones: 0},
		},
	}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func newID(prefix string, n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return prefix + hex.EncodeToString(b)
}

type httpError struct {
	status int
	code   string
	msg    string
}

func bad(format string, args ...any) *httpError {
	return &httpError{http.StatusBadRequest, "bad_request", fmt.Sprintf(format, args...)}
}

func notFound(format string, args ...any) *httpError {
	return &httpError{http.StatusNotFound, "not_found", fmt.Sprintf(format, args...)}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (f *fakeMast) serve(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests = append(f.requests, r.Method+" "+r.URL.Path)

	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	scopes, ok := f.tokens[token]
	if !ok {
		writeJSON(w, 401, map[string]any{"error": map[string]string{"code": "unauthorized", "message": "Invalid token."}})
		return
	}
	need := "mast:write"
	if r.Method == http.MethodGet {
		need = "mast:read"
	}
	if !slices.Contains(scopes, need) {
		writeJSON(w, 403, map[string]any{"error": map[string]string{
			"code": "invalid_scope", "message": fmt.Sprintf("Token missing scope '%s'.", need)}})
		return
	}

	status, out, herr := f.route(r)
	if herr != nil {
		writeJSON(w, herr.status, map[string]any{"error": map[string]string{"code": herr.code, "message": herr.msg}})
		return
	}
	if status == http.StatusNoContent {
		w.WriteHeader(status)
		return
	}
	writeJSON(w, status, out)
}

func (f *fakeMast) route(r *http.Request) (int, any, *httpError) {
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/v1/mast/"), "/")
	var body map[string]json.RawMessage
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil && err.Error() != "EOF" {
			return 0, nil, bad("Invalid JSON body: %v", err)
		}
	}
	m := r.Method
	switch {
	case parts[0] == "state" && m == "GET":
		var list []any
		for _, id := range f.order {
			if ch, ok := f.channels[id]; ok && !ch.archived {
				list = append(list, f.render(ch, true))
			}
		}
		return 200, map[string]any{"owner": map[string]any{}, "channels": list, "messages": []any{}}, nil
	case parts[0] == "responders" && m == "GET":
		return 200, map[string]any{"items": f.people}, nil
	case parts[0] == "channels" && len(parts) == 1 && m == "POST":
		return f.createChannel(body)
	case parts[0] == "channels" && len(parts) == 2:
		ch := f.resolveChannel(parts[1])
		if ch == nil {
			return 0, nil, notFound("No channel %q.", parts[1])
		}
		switch m {
		case "GET":
			return 200, f.render(ch, false), nil
		case "PATCH":
			return f.updateChannel(ch, body)
		case "DELETE":
			delete(f.channels, ch.ID)
			return 200, map[string]any{"ok": true}, nil
		}
	case parts[0] == "schedules" && len(parts) == 1 && m == "GET":
		items := []any{}
		for _, sc := range f.sortedSchedules() {
			items = append(items, f.renderSchedule(sc))
		}
		return 200, map[string]any{"items": items}, nil
	case parts[0] == "schedules" && len(parts) == 1 && m == "POST":
		sc := &apiSchedule{ID: newID("sc_", 16), Handoff: "weekly", HandoffTime: "09:00", TZ: "UTC",
			CreatedAt: time.Now().UTC().Format(time.RFC3339)}
		if herr := f.applySchedule(sc, body); herr != nil {
			return 0, nil, herr
		}
		f.schedules[sc.ID] = sc
		return 201, f.renderSchedule(sc), nil
	case parts[0] == "schedules" && len(parts) == 2:
		sc, ok := f.schedules[parts[1]]
		if !ok {
			return 0, nil, notFound("No schedule %q.", parts[1])
		}
		switch m {
		case "GET":
			return 200, f.renderSchedule(sc), nil
		case "PATCH":
			next := *sc
			next.Order = slices.Clone(sc.Order)
			next.Overrides = slices.Clone(sc.Overrides)
			if herr := f.applySchedule(&next, body); herr != nil {
				return 0, nil, herr
			}
			if f.schedulePatchGap > 0 {
				f.mu.Unlock()
				time.Sleep(f.schedulePatchGap)
				f.mu.Lock()
			}
			*sc = next
			return 200, f.renderSchedule(sc), nil
		case "DELETE":
			delete(f.schedules, sc.ID)
			return 204, nil, nil
		}
	case parts[0] == "policies" && len(parts) == 1 && m == "GET":
		items := []any{}
		for _, po := range f.policies {
			items = append(items, f.renderPolicy(po))
		}
		return 200, map[string]any{"items": items}, nil
	case parts[0] == "policies" && len(parts) == 1 && m == "POST":
		po := &fakePolicy{ID: newID("po_", 16), CreatedAt: time.Now().UTC().Format(time.RFC3339)}
		if herr := f.applyPolicy(po, body, true); herr != nil {
			return 0, nil, herr
		}
		f.policies[po.ID] = po
		return 201, f.renderPolicy(po), nil
	case parts[0] == "policies" && len(parts) == 2:
		po, ok := f.policies[parts[1]]
		if !ok {
			return 0, nil, notFound("No policy %q.", parts[1])
		}
		switch m {
		case "GET":
			return 200, f.renderPolicy(po), nil
		case "PATCH":
			next := *po
			if herr := f.applyPolicy(&next, body, false); herr != nil {
				return 0, nil, herr
			}
			*po = next
			return 200, f.renderPolicy(po), nil
		case "DELETE":
			delete(f.policies, po.ID)
			for _, ch := range f.channels {
				if ch.policy == po.ID {
					ch.policy = ""
				}
			}
			return 204, nil, nil
		}
	}
	return 0, nil, notFound("No route %s %s.", m, r.URL.Path)
}

func (f *fakeMast) resolveChannel(idOrName string) *fakeChannel {
	if ch, ok := f.channels[idOrName]; ok {
		return ch
	}
	name := norm.NFC.String(strings.TrimSpace(idOrName))
	for _, ch := range f.channels {
		if ch.Name == name {
			return ch
		}
	}
	return nil
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// render mirrors channel_json: the key and URL only travel when asked for,
// and empty optional strings are null.
func (f *fakeMast) render(ch *fakeChannel, withKey bool) map[string]any {
	out := map[string]any{
		"id": ch.ID, "name": ch.Name, "kind": ch.Kind, "created_at": ch.CreatedAt,
		"archived_at": nil, "capped_at": nil, "url": nil, "key": nil,
		"default_priority": ch.DefaultPriority, "max_priority": ch.MaxPriority,
		"sound": nullable(str(ch.Sound)), "muted_until": nil,
		"quiet_start": ch.QuietStart, "quiet_end": ch.QuietEnd, "quiet_tz": nullable(str(ch.QuietTZ)),
		"dedupe_window_secs": ch.DedupeWindowSecs, "storm_threshold": ch.StormThreshold,
		"storm_window_secs": ch.StormWindowSecs, "repeat_secs": ch.RepeatSecs, "expire_secs": ch.ExpireSecs,
		"escalate_after_secs": ch.EscalateAfterSecs, "escalate_to_owner_id": nil,
		"period_secs": ch.PeriodSecs, "grace_secs": ch.GraceSecs,
		"schedule": nullable(str(ch.Schedule)), "schedule_tz": nullable(str(ch.ScheduleTZ)),
		"max_duration_secs": ch.MaxDurationSecs, "ack_timeout_min": ch.AckTimeoutMin,
		"canary_url": str(ch.CanaryURL), "vital_state": ch.VitalState, "vital_priority": ch.VitalPriority,
		"fail_threshold": ch.FailThreshold, "smart_early_warn": ch.SmartEarlyWarn, "host": nullable(str(ch.Host)),
		"message_count": 0,
	}
	if ch.archived {
		out["archived_at"] = "2026-10-01T00:00:00.000Z"
	}
	if withKey {
		out["key"] = ch.key
		out["url"] = "https://mast.tissue.dev/" + ch.key
	}
	return out
}

func decode[T any](body map[string]json.RawMessage, key string) (*T, *httpError) {
	raw, ok := body[key]
	if !ok || string(raw) == "null" {
		return nil, nil
	}
	var v T
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, bad("Invalid JSON body: %s: %v", key, err)
	}
	return &v, nil
}

func priority(v *string, def, field string) (string, *httpError) {
	p := def
	if v != nil {
		p = strings.TrimSpace(*v)
	}
	if !slices.Contains([]string{"quiet", "normal", "loud", "page"}, p) {
		return "", bad("%s must be quiet, normal, loud or page (got %q).", field, p)
	}
	return p, nil
}

func canonicalSound(s string) (string, *httpError) {
	if s == "" {
		return "", nil
	}
	stem := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(s)), ".caf")
	if !slices.Contains([]string{"default", "bleep", "chirp", "klaxon", "pager", "rising"}, stem) {
		return "", bad("Field 'sound' must be one of: default, bleep, chirp, klaxon, pager, rising.")
	}
	if stem == "default" {
		return stem, nil
	}
	return stem + ".caf", nil
}

func ptr(s string) *string { return &s }

// applyChannel applies the fields both create and update take.
func (f *fakeMast) applyChannel(ch *fakeChannel, body map[string]json.RawMessage) *httpError {
	ints := map[string]struct {
		dst      *int64
		min, max int64
	}{
		"quiet_start":         {&ch.QuietStart, -1, 1439},
		"quiet_end":           {&ch.QuietEnd, -1, 1439},
		"dedupe_window_secs":  {&ch.DedupeWindowSecs, 0, 86400},
		"storm_threshold":     {&ch.StormThreshold, 0, 10000},
		"storm_window_secs":   {&ch.StormWindowSecs, 0, 86400},
		"repeat_secs":         {&ch.RepeatSecs, 0, 86400},
		"expire_secs":         {&ch.ExpireSecs, 0, 604800},
		"escalate_after_secs": {&ch.EscalateAfterSecs, 0, 86400},
		"period_secs":         {&ch.PeriodSecs, 0, 2592000},
		"grace_secs":          {&ch.GraceSecs, 0, 2592000},
		"max_duration_secs":   {&ch.MaxDurationSecs, 0, 2592000},
		"fail_threshold":      {&ch.FailThreshold, 1, 100},
	}
	for key, spec := range ints {
		v, herr := decode[int64](body, key)
		if herr != nil {
			return herr
		}
		if v == nil {
			continue
		}
		if *v < spec.min || *v > spec.max {
			return bad("%s must be between %d and %d.", key, spec.min, spec.max)
		}
		*spec.dst = *v
	}
	if v, herr := decode[string](body, "default_priority"); herr != nil {
		return herr
	} else if v != nil {
		p, herr := priority(v, "normal", "default_priority")
		if herr != nil {
			return herr
		}
		ch.DefaultPriority = p
	}
	if v, herr := decode[string](body, "vital_priority"); herr != nil {
		return herr
	} else if v != nil {
		p, herr := priority(v, "loud", "vital_priority")
		if herr != nil {
			return herr
		}
		ch.VitalPriority = p
	}
	if v, _ := decode[string](body, "sound"); v != nil {
		s, herr := canonicalSound(*v)
		if herr != nil {
			return herr
		}
		ch.Sound = ptr(s)
	}
	if v, _ := decode[string](body, "quiet_tz"); v != nil {
		ch.QuietTZ = v
	}
	for key, dst := range map[string]**string{"schedule": &ch.Schedule, "schedule_tz": &ch.ScheduleTZ, "host": &ch.Host} {
		if v, _ := decode[string](body, key); v != nil {
			*dst = ptr(strings.TrimSpace(*v))
		}
	}
	if v, _ := decode[bool](body, "smart_early_warn"); v != nil {
		ch.SmartEarlyWarn = *v
	}
	if str(ch.Schedule) != "" && ch.GraceSecs <= 0 {
		return bad("A schedule needs grace_secs above 0.")
	}
	return nil
}

func (f *fakeMast) createChannel(body map[string]json.RawMessage) (int, any, *httpError) {
	ch := &fakeChannel{key: newID("mk_", 20)}
	ch.ID = newID("mc_", 16)
	ch.Kind = "channel"
	if k, _ := decode[string](body, "kind"); k != nil {
		ch.Kind = strings.TrimSpace(*k)
	}
	if ch.Kind != "channel" && ch.Kind != "vital" {
		return 0, nil, bad("kind must be \"channel\" or \"vital\" (got %q).", ch.Kind)
	}
	name, _ := decode[string](body, "name")
	if name == nil {
		return 0, nil, bad("name is required.")
	}
	ch.Name = norm.NFC.String(strings.TrimSpace(*name))
	if p := channelNameProblem(ch.Name); p != "" {
		return 0, nil, bad("%s", p)
	}
	if f.resolveChannel(ch.Name) != nil {
		return 0, nil, &httpError{409, "conflict", fmt.Sprintf("A channel named %q already exists.", ch.Name)}
	}
	ch.CreatedAt = time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
	ch.DefaultPriority, ch.MaxPriority, ch.VitalPriority = "normal", "loud", "loud"
	ch.QuietStart, ch.QuietEnd = -1, -1
	ch.DedupeWindowSecs, ch.StormThreshold, ch.StormWindowSecs = 300, 20, 300
	ch.RepeatSecs, ch.ExpireSecs, ch.FailThreshold = 120, 3600, 1
	ch.CanaryURL = ptr("")
	if ch.Kind == "vital" {
		ch.VitalState = "alive"
	}
	mp, herr := decode[string](body, "max_priority")
	if herr != nil {
		return 0, nil, herr
	}
	if mp != nil {
		p, herr := priority(mp, "loud", "max_priority")
		if herr != nil {
			return 0, nil, herr
		}
		if p == "page" {
			return 0, nil, bad("Field 'max_priority' can only be set from the app on a registered device.")
		}
		ch.MaxPriority = p
	}
	if herr := f.applyChannel(ch, body); herr != nil {
		return 0, nil, herr
	}
	f.channels[ch.ID] = ch
	f.order = append(f.order, ch.ID)
	out := f.render(ch, true)
	out["last_message_at"] = nil
	return 201, out, nil
}

func (f *fakeMast) updateChannel(ch *fakeChannel, body map[string]json.RawMessage) (int, any, *httpError) {
	if _, ok := body["max_priority"]; ok {
		return 0, nil, bad("Field 'max_priority' can only be set from the app on a registered device.")
	}
	next := *ch
	if name, _ := decode[string](body, "name"); name != nil {
		n := norm.NFC.String(strings.TrimSpace(*name))
		if p := channelNameProblem(n); p != "" {
			return 0, nil, bad("%s", p)
		}
		if other := f.resolveChannel(n); other != nil && other.ID != ch.ID {
			return 0, nil, &httpError{409, "conflict", fmt.Sprintf("A channel named %q already exists.", n)}
		}
		next.Name = n
	}
	if herr := f.applyChannel(&next, body); herr != nil {
		return 0, nil, herr
	}
	if v, _ := decode[int64](body, "ack_timeout_min"); v != nil {
		if *v < 0 || *v > 10080 {
			return 0, nil, bad("ack_timeout_min must be between 0 and 10080.")
		}
		next.AckTimeoutMin = *v
	}
	if v, _ := decode[string](body, "canary_url"); v != nil {
		next.CanaryURL = ptr(strings.TrimSpace(*v))
	}
	if v, _ := decode[string](body, "vital_state"); v != nil {
		if ch.Kind != "vital" {
			return 0, nil, bad("Field 'vital_state' applies to a vital, not a channel.")
		}
		next.VitalState = *v
	}
	if v, _ := decode[bool](body, "archived"); v != nil {
		next.archived = *v
	}
	policy, _ := decode[string](body, "policy_id")
	if policy != nil && *policy != "" {
		if _, ok := f.policies[*policy]; !ok {
			return 0, nil, notFound("No policy %q.", *policy)
		}
	}
	if policy != nil {
		next.policy = *policy
	}
	*ch = next
	out := f.render(ch, true)
	if policy != nil {
		out["policy_id"] = nullable(*policy)
	}
	return 200, out, nil
}

func (f *fakeMast) sortedSchedules() []*apiSchedule {
	list := make([]*apiSchedule, 0, len(f.schedules))
	for _, sc := range f.schedules {
		list = append(list, sc)
	}
	slices.SortFunc(list, func(a, b *apiSchedule) int { return strings.Compare(a.ID, b.ID) })
	return list
}

func (f *fakeMast) member(id string) bool {
	return slices.ContainsFunc(f.people, func(p apiPerson) bool { return p.UserID == id })
}

// cleanName is mast_web.rs's clean_name: trimmed, at most 80 characters
// (not bytes), no control characters.
func cleanName(v string) (string, *httpError) {
	v = strings.TrimSpace(v)
	if utf8.RuneCountInString(v) > 80 {
		return "", bad("name is at most 80 characters.")
	}
	if strings.ContainsFunc(v, unicode.IsControl) {
		return "", bad("name cannot contain control characters.")
	}
	return v, nil
}

func (f *fakeMast) applySchedule(sc *apiSchedule, body map[string]json.RawMessage) *httpError {
	if v, _ := decode[string](body, "name"); v != nil {
		name, herr := cleanName(*v)
		if herr != nil {
			return herr
		}
		sc.Name = name
	}
	if sc.Name == "" {
		return bad("name is required.")
	}
	if v, _ := decode[string](body, "handoff"); v != nil {
		sc.Handoff = strings.ToLower(strings.TrimSpace(*v))
	}
	if !slices.Contains([]string{"daily", "weekly", "biweekly", "custom"}, sc.Handoff) {
		return bad("handoff is daily, weekly, biweekly or custom.")
	}
	length, _ := decode[int64](body, "length_hours")
	if sc.Handoff == "custom" {
		if length != nil {
			sc.LengthHours = *length
		}
		if sc.LengthHours < 1 || sc.LengthHours > 720 {
			return bad("length_hours is 1 to 720 for a custom handoff.")
		}
	} else {
		if length != nil && *length != 0 {
			return bad("length_hours is only for a custom handoff; leave it out or send 0.")
		}
		sc.LengthHours = 0
	}
	if v, _ := decode[int64](body, "handoff_day"); v != nil {
		sc.HandoffDay = *v
	}
	if sc.HandoffDay < 0 || sc.HandoffDay > 6 {
		return bad("handoff_day is 0 (Monday) to 6 (Sunday).")
	}
	if v, _ := decode[string](body, "handoff_time"); v != nil {
		sc.HandoffTime = strings.TrimSpace(*v)
	}
	if !hhmm.MatchString(sc.HandoffTime) {
		return bad("handoff_time is HH:MM, 24-hour.")
	}
	if v, _ := decode[string](body, "tz"); v != nil {
		sc.TZ = strings.TrimSpace(*v)
	}
	if sc.TZ == "" {
		sc.TZ = "UTC"
	}
	if _, err := time.LoadLocation(sc.TZ); err != nil && !strings.EqualFold(sc.TZ, "utc") {
		return bad("Unknown time zone %q.", sc.TZ)
	}
	if v, herr := decode[[]string](body, "order"); herr != nil {
		return herr
	} else if v != nil {
		for _, id := range *v {
			if !f.member(id) {
				return bad("%q is not a member of this account.", id)
			}
		}
		sc.Order = *v
	}
	if v, herr := decode[[]apiOverride](body, "overrides"); herr != nil {
		return herr
	} else if v != nil {
		list := *v
		for i := range list {
			o := &list[i]
			start, ok1 := overrideStart(o.Start)
			end, ok2 := overrideEnd(o.End)
			if o.UserID == "" {
				return bad("each override needs a user_id")
			}
			if !ok1 || !ok2 {
				return bad("override start and end are dates, YYYY-MM-DD, or local times, YYYY-MM-DDTHH:MM")
			}
			if !end.After(start) {
				return bad("an override has to end after it starts")
			}
			if !f.member(o.UserID) || (o.CoversUserID != "" && !f.member(o.CoversUserID)) {
				return bad("%q is not a member of this account.", o.UserID)
			}
			if o.ID == "" {
				o.ID = newID("ov_", 16)
			}
		}
		sc.Overrides = list
	}
	return nil
}

func (f *fakeMast) renderSchedule(sc *apiSchedule) map[string]any {
	order := sc.Order
	if order == nil {
		order = []string{}
	}
	overrides := sc.Overrides
	if overrides == nil {
		overrides = []apiOverride{}
	}
	var now any
	if len(order) > 0 {
		now = map[string]any{"user_id": order[0], "until": "2026-11-02T09:00:00Z"}
	}
	return map[string]any{
		"id": sc.ID, "name": sc.Name, "handoff": sc.Handoff, "handoff_day": sc.HandoffDay,
		"handoff_time": sc.HandoffTime, "length_hours": sc.LengthHours, "tz": sc.TZ,
		"order": order, "overrides": overrides, "calendar_token": nil, "capped_at": nil,
		"now": now, "upcoming": []any{}, "created_at": sc.CreatedAt,
	}
}

func (f *fakeMast) applyPolicy(po *fakePolicy, body map[string]json.RawMessage, create bool) *httpError {
	if v, _ := decode[string](body, "name"); v != nil {
		name, herr := cleanName(*v)
		if herr != nil {
			return herr
		}
		po.Name = name
	}
	if po.Name == "" {
		return bad("name is required.")
	}
	steps, herr := decode[[]apiStep](body, "steps")
	if herr != nil {
		return herr
	}
	if steps != nil {
		if len(*steps) == 0 {
			return bad("A policy needs at least one step.")
		}
		list := *steps
		for i := range list {
			s := &list[i]
			n := i + 1
			if s.AfterSecs < 0 || s.AfterSecs > policyAfterMax {
				return bad("Step %d: after_secs is 0 to %d.", n, policyAfterMax)
			}
			id := ""
			if s.Target.ID != nil {
				id = strings.TrimSpace(*s.Target.ID)
			}
			switch s.Target.Type {
			case "channel":
				if id == "" {
					return bad("Step %d: a channel target needs an id.", n)
				}
				ch := f.resolveChannel(id)
				if ch == nil {
					return notFound("No channel %q.", id)
				}
				s.Target.ID = ptr(ch.ID)
			case "user":
				if id == "" {
					return bad("Step %d: a user target needs an id.", n)
				}
			case "oncall", "next":
				if id == "" {
					s.Target.ID = nil
					break
				}
				if _, ok := f.schedules[id]; !ok {
					return notFound("No schedule %q.", id)
				}
			case "everyone":
				s.Target.ID = nil
			default:
				return bad("Step %d: target type is one of oncall, next, everyone, user, channel.", n)
			}
			if !slices.Contains([]string{"page", "loud", "quiet"}, s.Priority) {
				return bad("Step %d: priority is page, loud or quiet.", n)
			}
		}
		po.Steps = list
	} else if create {
		return bad("A policy needs at least one step.")
	}

	if raw, ok := body["repeat_every_secs"]; ok {
		var secs int64
		if string(raw) != "null" {
			if err := json.Unmarshal(raw, &secs); err != nil {
				return bad("Invalid JSON body: %v", err)
			}
		}
		if secs != 0 && (secs < 60 || secs > policyAfterMax) {
			return bad("repeat_every_secs is 60 to %d, or null.", policyAfterMax)
		}
		po.RepeatEverySecs = secs
	}
	sent, _ := decode[int64](body, "repeat_count")
	_, present := body["repeat_count"]
	count := int64(0)
	if sent != nil {
		count = *sent
	}
	if count != 0 && (count < 1 || count > 10) {
		return bad("repeat_count is 1 to 10, or null.")
	}
	if po.RepeatEverySecs == 0 {
		if count != 0 {
			return bad("repeat_count needs repeat_every_secs.")
		}
		po.RepeatCount = 0
		return nil
	}
	if !present {
		count = po.RepeatCount
	}
	if count == 0 {
		count = 3
	}
	po.RepeatCount = count
	return nil
}

func (f *fakeMast) renderPolicy(po *fakePolicy) map[string]any {
	channels := []string{}
	for _, id := range f.order {
		if ch, ok := f.channels[id]; ok && ch.policy == po.ID {
			channels = append(channels, id)
		}
	}
	var every, count any
	if po.RepeatEverySecs != 0 {
		every = po.RepeatEverySecs
	}
	if po.RepeatCount != 0 {
		count = po.RepeatCount
	}
	return map[string]any{
		"id": po.ID, "name": po.Name, "steps": po.Steps, "repeat_every_secs": every, "repeat_count": count,
		"capped_at": nil, "used_by": map[string]any{"channels": channels, "probes": []string{}},
		"created_at": po.CreatedAt,
	}
}

// count says how many requests the fake has answered for "METHOD /path".
func (f *fakeMast) count(route string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, r := range f.requests {
		if r == route {
			n++
		}
	}
	return n
}

// app changes a channel the way someone in the Mast app would.
func (f *fakeMast) app(name string, change func(*fakeChannel)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	ch := f.resolveChannel(name)
	if ch == nil {
		f.t.Fatalf("no channel %q in the fake", name)
	}
	change(ch)
}
