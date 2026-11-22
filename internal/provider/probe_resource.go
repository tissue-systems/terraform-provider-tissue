package provider

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringdefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

type probeResource struct {
	client *Client
}

func newProbeResource() resource.Resource { return &probeResource{} }

type probeModel struct {
	ID                 types.String    `tfsdk:"id"`
	Name               types.String    `tfsdk:"name"`
	Kind               types.String    `tfsdk:"kind"`
	Target             types.String    `tfsdk:"target"`
	Method             types.String    `tfsdk:"method"`
	IntervalSecs       types.Int64     `tfsdk:"interval_secs"`
	Expect             types.String    `tfsdk:"expect"`
	ExpectStatus       types.Int64     `tfsdk:"expect_status"`
	ExpectStatuses     types.String    `tfsdk:"expect_statuses"`
	ExpectHeader       types.String    `tfsdk:"expect_header"`
	BodyContains       types.String    `tfsdk:"body_contains"`
	BodyNotContains    types.String    `tfsdk:"body_not_contains"`
	JSONPath           types.String    `tfsdk:"json_path"`
	ExpectValue        types.String    `tfsdk:"expect_value"`
	ExpectSet          types.String    `tfsdk:"expect_set"`
	IgnoreCase         types.Bool      `tfsdk:"ignore_case"`
	Resolver           types.String    `tfsdk:"resolver"`
	Send               types.String    `tfsdk:"send"`
	RequestHeaders     types.List      `tfsdk:"request_headers"`
	RequestBody        types.String    `tfsdk:"request_body"`
	RequestContentType types.String    `tfsdk:"request_content_type"`
	Auth               *probeAuthModel `tfsdk:"auth"`
	MaxMs              types.Int64     `tfsdk:"max_ms"`
	AlertSourceID      types.String    `tfsdk:"alert_source_id"`
	ChannelID          types.String    `tfsdk:"channel_id"`
	PolicyID           types.String    `tfsdk:"policy_id"`
	Host               types.String    `tfsdk:"host"`
	Paused             types.Bool      `tfsdk:"paused"`
	DomainExpiry       types.Bool      `tfsdk:"domain_expiry"`
	CreatedAt          types.String    `tfsdk:"created_at"`
}

type probeAuthModel struct {
	Type     types.String `tfsdk:"type"`
	Username types.String `tfsdk:"username"`
	Password types.String `tfsdk:"password"`
	Token    types.String `tfsdk:"token"`
}

// The API's name for each field that is a string, a number or a boolean.
// Sending "" or 0 takes a value away again. alert_source_id isn't here: the
// plan holds it equal to channel_id, and the request carries channel_id.
func (m *probeModel) fields() map[string]attr.Value {
	return map[string]attr.Value{
		"name":                 m.Name,
		"target":               m.Target,
		"method":               m.Method,
		"interval_secs":        m.IntervalSecs,
		"expect":               m.Expect,
		"expect_status":        m.ExpectStatus,
		"expect_statuses":      m.ExpectStatuses,
		"expect_header":        m.ExpectHeader,
		"body_contains":        m.BodyContains,
		"body_not_contains":    m.BodyNotContains,
		"json_path":            m.JSONPath,
		"expect_value":         m.ExpectValue,
		"expect_set":           m.ExpectSet,
		"ignore_case":          m.IgnoreCase,
		"resolver":             m.Resolver,
		"send":                 m.Send,
		"request_body":         m.RequestBody,
		"request_content_type": m.RequestContentType,
		"max_ms":               m.MaxMs,
		"channel_id":           m.ChannelID,
		"policy_id":            m.PolicyID,
		"host":                 m.Host,
		"paused":               m.Paused,
		"domain_expiry":        m.DomainExpiry,
	}
}

var (
	probeKinds       = []string{"http", "tcp", "tls", "dns", "icmp", "smtp"}
	probeHTTPMethods = []string{"GET", "HEAD", "POST", "PUT", "PATCH"}
	probeDNSTypes    = []string{"A", "AAAA", "CNAME", "MX", "TXT", "NS"}
	probeSMTPSteps   = []string{"EHLO", "STARTTLS"}
)

// Which kinds read each argument. The server refuses one set on any other
// kind; name, target, interval_secs, expect, max_ms and the rest apply to all.
var probeKindFields = map[string][]string{
	"method":               {"http", "dns", "smtp"},
	"expect_status":        {"http"},
	"expect_statuses":      {"http"},
	"expect_header":        {"http"},
	"body_contains":        {"http", "dns", "smtp", "tcp"},
	"body_not_contains":    {"http"},
	"json_path":            {"http"},
	"expect_value":         {"http", "dns"},
	"expect_set":           {"dns"},
	"resolver":             {"dns"},
	"send":                 {"tcp"},
	"request_headers":      {"http"},
	"request_body":         {"http"},
	"request_content_type": {"http"},
	"auth":                 {"http"},
}

func (r *probeResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_mast_probe"
}

// A text condition the server stores as written.
func probeText(desc string, max int) schema.StringAttribute {
	return schema.StringAttribute{
		MarkdownDescription: desc,
		Optional:            true,
		Validators:          []validator.String{stringvalidator.LengthBetween(1, max)},
	}
}

// A text condition the server trims.
func probeTrimmed(desc string, max int) schema.StringAttribute {
	return schema.StringAttribute{
		MarkdownDescription: desc,
		Optional:            true,
		Validators:          []validator.String{stringvalidator.LengthBetween(1, max), trimmedValidator{}},
	}
}

func (r *probeResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A probe: Mast checks a URL, port, certificate, DNS name, host or mail server from " +
			"several locations on a schedule, and pages when they agree it is down. The conditions you leave out " +
			"are not checked, so the configuration is the whole check. `name`, `interval_secs`, `paused` and " +
			"`domain_expiry` follow the app when you leave them out.",
		Attributes: map[string]schema.Attribute{
			"id": computedString("The probe id, `pr_...`."),
			"name": optionalString("The name shown in the app and on a page. Defaults to the target's host name.",
				stringvalidator.UTF8LengthBetween(1, 80), trimmedValidator{}),
			"kind": schema.StringAttribute{
				MarkdownDescription: "What the probe does: `http` (the default), `tcp`, `tls`, `dns`, `icmp` or `smtp`. " +
					"Changing it replaces the probe.",
				Optional:      true,
				Computed:      true,
				Default:       stringdefault.StaticString("http"),
				Validators:    []validator.String{stringvalidator.OneOf(probeKinds...)},
				PlanModifiers: []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"target": schema.StringAttribute{
				MarkdownDescription: "What to check: a URL for `http`, `host:port` for `tcp`, `tls` and `smtp`, a name " +
					"for `dns`, a host for `icmp`. Mast keeps one probe per check, so a second probe with the same " +
					"kind, target, method and conditions is refused.",
				Required:   true,
				Validators: []validator.String{stringvalidator.LengthBetween(1, 2048), trimmedValidator{}},
			},
			"method": schema.StringAttribute{
				MarkdownDescription: "For `http` the request method: `GET` (the default), `HEAD`, `POST`, `PUT` or `PATCH`. " +
					"For `dns` the record type: `A` (the default), `AAAA`, `CNAME`, `MX`, `TXT` or `NS`. For `smtp` how far " +
					"to go after the banner: `EHLO` or `STARTTLS`; leave it out to stop at the banner.",
				Optional: true,
				Computed: true,
			},
			"interval_secs": optionalInt("Seconds between checks, 300 unless you say otherwise. The plan sets the "+
				"shortest interval, and a probe on port 25 checks at most every 300 seconds.",
				int64validator.Between(30, 86400)),
			"expect": schema.StringAttribute{
				MarkdownDescription: "`up` (the default), or `down` for a target that should not answer: the probe is " +
					"fine while the check fails and pages when it succeeds.",
				Optional:   true,
				Computed:   true,
				Default:    stringdefault.StaticString("up"),
				Validators: []validator.String{stringvalidator.OneOf("up", "down")},
			},
			"expect_status": schema.Int64Attribute{
				MarkdownDescription: "`http`: the one status that counts as up. Without it, or `expect_statuses`, any " +
					"2xx or 3xx does.",
				Optional:   true,
				Validators: []validator.Int64{int64validator.Between(100, 599)},
			},
			"expect_statuses": probeTrimmed("`http`: the statuses that count as up, comma-separated, such as `200,204`. "+
				"Replaces `expect_status`.", 200),
			"expect_header": probeTrimmed("`http`: `Name: text`, a header the response must carry with `text` in "+
				"its value. `Name:` alone only asks that it's there.", 1024),
			"body_contains": probeText("Text the answer must contain: the response body for `http`, a record for `dns`, "+
				"the server's reply for `smtp` and `tcp`.", 200),
			"body_not_contains": probeText("`http`: text the response body must not contain.", 200),
			"json_path":         probeTrimmed("`http`: a path into a JSON response body, `/a/0/b` or `a.0.b`, that must exist.", 200),
			"expect_value": probeTrimmed("`http`: the value at `json_path`, as JSON or a bare string. `dns`: a record "+
				"the answer must hold.", 1024),
			"expect_set": probeTrimmed("`dns`: the exact set of records the answer must hold, comma-separated.", 1024),
			"ignore_case": schema.BoolAttribute{
				MarkdownDescription: "Match text without regard to case.",
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(false),
			},
			"resolver": probeTrimmed("`dns`: the resolver to ask, a public IP address with an optional port.", 255),
			"send": probeText("`tcp`: text to send once connected, with `\\r`, `\\n`, `\\t` and `\\\\` decoded. "+
				"The reply is matched against `body_contains`.", 1024),
			"request_headers": schema.ListAttribute{
				MarkdownDescription: "`http`: headers to send, each one `Name: value`, at most 10.",
				Optional:            true,
				ElementType:         types.StringType,
				Validators:          []validator.List{listvalidator.SizeBetween(1, 10)},
			},
			"request_body": probeText("`http` with `POST`, `PUT` or `PATCH`: the body to send, up to 16 KiB. "+
				"Mast sends one only to a domain you have proven yours.", 16*1024),
			"request_content_type": schema.StringAttribute{
				MarkdownDescription: "`http`: the Content-Type of `request_body`. `application/json` when a body comes without one.",
				Optional:            true,
				Computed:            true,
				Validators:          []validator.String{stringvalidator.LengthAtLeast(1), trimmedValidator{}},
			},
			"auth": schema.SingleNestedAttribute{
				MarkdownDescription: "`http`: credentials to send. Mast never gives the secret back, so Terraform can't " +
					"see it change in the app, and after an import the first apply sends it again. Mast sends " +
					"credentials only to a domain you have proven yours.",
				Optional: true,
				Attributes: map[string]schema.Attribute{
					"type": schema.StringAttribute{
						MarkdownDescription: "`basic` or `bearer`.",
						Required:            true,
						Validators:          []validator.String{stringvalidator.OneOf("basic", "bearer")},
					},
					"username": schema.StringAttribute{
						MarkdownDescription: "`basic`: the user name.",
						Optional:            true,
						Validators:          []validator.String{stringvalidator.LengthAtLeast(1)},
					},
					"password": schema.StringAttribute{
						MarkdownDescription: "`basic`: the password.",
						Optional:            true,
						Sensitive:           true,
						Validators:          []validator.String{stringvalidator.LengthBetween(1, 4096)},
					},
					"token": schema.StringAttribute{
						MarkdownDescription: "`bearer`: the token.",
						Optional:            true,
						Sensitive:           true,
						Validators:          []validator.String{stringvalidator.LengthBetween(1, 4096)},
					},
				},
			},
			"max_ms": schema.Int64Attribute{
				MarkdownDescription: "An answer that takes longer than this many milliseconds, up to 60000, is `slow`: it shows on " +
					"the probe and pages nobody.",
				Optional:   true,
				Validators: []validator.Int64{int64validator.Between(1, 60000)},
			},
			"alert_source_id": schema.StringAttribute{
				MarkdownDescription: "The alert source the probe's pages go to, `mc_...`.",
				Optional:            true,
				Computed:            true,
				Validators: []validator.String{
					stringvalidator.RegexMatches(regexp.MustCompile(`^mc_`), "must be an alert source id, mc_..."),
					stringvalidator.ConflictsWith(path.MatchRoot("channel_id")),
				},
				PlanModifiers: []planmodifier.String{mirrors{other: "channel_id"}},
			},
			"channel_id": schema.StringAttribute{
				MarkdownDescription: "The deprecated name of `alert_source_id`.",
				DeprecationMessage:  "Use alert_source_id. It takes the same value.",
				Optional:            true,
				Computed:            true,
				Validators: []validator.String{
					stringvalidator.RegexMatches(regexp.MustCompile(`^mc_`), "must be an alert source id, mc_..."),
				},
				PlanModifiers: []planmodifier.String{mirrors{other: "alert_source_id"}},
			},
			"policy_id": schema.StringAttribute{
				MarkdownDescription: "The escalation policy the probe's pages follow, `po_...`.",
				Optional:            true,
				Validators: []validator.String{
					stringvalidator.RegexMatches(regexp.MustCompile(`^po_`), "must be a policy id, po_..."),
				},
			},
			"host": schema.StringAttribute{
				MarkdownDescription: "The machine the target runs on, such as `web1`, so a page can say what else is " +
					"wrong on it. Letters, digits, `.`, `_` and `-`.",
				Optional: true,
				Validators: []validator.String{
					stringvalidator.RegexMatches(regexp.MustCompile(`^[A-Za-z0-9._-]+$`), "letters, digits, '.', '_' and '-' only"),
				},
			},
			"paused": schema.BoolAttribute{
				MarkdownDescription: "A paused probe makes no checks and sends nothing.",
				Optional:            true,
				Computed:            true,
				PlanModifiers:       []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
			},
			"domain_expiry": schema.BoolAttribute{
				MarkdownDescription: "Whether Mast watches the registration of the target's domain and sends a notice " +
					"when it has 30, 14 and 7 days left. On unless you turn it off; `true` is refused on a plan " +
					"without domain expiry notices and for a target with no registered domain, such as an IP address.",
				Optional:      true,
				Computed:      true,
				PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
			},
			"created_at": computedString("When the probe was made."),
		},
	}
}

// mirrors keeps an attribute equal to the other name for the same setting.
// Whichever of the two the configuration sets decides both, and with neither
// set both are null, which takes the setting away.
type mirrors struct{ other string }

func (m mirrors) Description(context.Context) string {
	return "holds the same value as " + m.other
}

func (m mirrors) MarkdownDescription(ctx context.Context) string { return m.Description(ctx) }

func (m mirrors) PlanModifyString(ctx context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	if !req.ConfigValue.IsNull() {
		return
	}
	var other types.String
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root(m.other), &other)...)
	resp.PlanValue = other
}

func (r *probeResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if c, ok := req.ProviderData.(*Client); ok {
		r.client = c
	}
}

// probeHeaderProblem holds a request header to the form the server stores,
// `Name: value` with one space, so what comes back matches the configuration.
func probeHeaderProblem(line string) string {
	if strings.ContainsAny(line, "\r\n") {
		return "is more than one line"
	}
	name, value, ok := strings.Cut(line, ":")
	if !ok || strings.TrimSpace(name) == "" {
		return "is not a \"Name: value\" line"
	}
	want := strings.TrimRight(strings.TrimSpace(name)+": "+strings.TrimSpace(value), " ")
	if line != want {
		return fmt.Sprintf("is stored as %q; write it that way", want)
	}
	return ""
}

func (r *probeResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg probeModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var lines []types.String
	if !cfg.RequestHeaders.IsNull() && !cfg.RequestHeaders.IsUnknown() {
		resp.Diagnostics.Append(cfg.RequestHeaders.ElementsAs(ctx, &lines, false)...)
	}
	for i, line := range lines {
		if line.IsNull() || line.IsUnknown() {
			continue
		}
		if p := probeHeaderProblem(line.ValueString()); p != "" {
			resp.Diagnostics.AddAttributeError(path.Root("request_headers").AtListIndex(i), "Invalid request header",
				fmt.Sprintf("%q %s.", line.ValueString(), p))
		}
	}

	if a := cfg.Auth; a != nil && !a.Type.IsUnknown() {
		at := path.Root("auth")
		switch a.Type.ValueString() {
		case "basic":
			if a.Username.IsNull() || a.Password.IsNull() {
				resp.Diagnostics.AddAttributeError(at, "Basic auth needs username and password", "Set both.")
			}
			if !a.Token.IsNull() {
				resp.Diagnostics.AddAttributeError(at.AtName("token"), "Not used by basic auth", "Remove token, or use type = \"bearer\".")
			}
		case "bearer":
			if a.Token.IsNull() {
				resp.Diagnostics.AddAttributeError(at, "Bearer auth needs token", "Set token.")
			}
			if !a.Username.IsNull() || !a.Password.IsNull() {
				resp.Diagnostics.AddAttributeError(at, "Not used by bearer auth", "Remove username and password, or use type = \"basic\".")
			}
		}
	}

	if cfg.Kind.IsUnknown() {
		return
	}
	kind := "http"
	if !cfg.Kind.IsNull() {
		kind = cfg.Kind.ValueString()
	}
	set := map[string]bool{"request_headers": !cfg.RequestHeaders.IsNull(), "auth": cfg.Auth != nil}
	for name, v := range cfg.fields() {
		set[name] = !v.IsNull()
	}
	for name, kinds := range probeKindFields {
		if set[name] && !slices.Contains(kinds, kind) {
			resp.Diagnostics.AddAttributeError(path.Root(name), "Not used by this kind of probe",
				fmt.Sprintf("%s applies to %s probes, and this one is %s.", name, strings.Join(kinds, ", "), kind))
		}
	}

	method := ""
	if !cfg.Method.IsNull() && !cfg.Method.IsUnknown() {
		method = cfg.Method.ValueString()
		allowed := map[string][]string{"http": probeHTTPMethods, "dns": probeDNSTypes, "smtp": probeSMTPSteps}[kind]
		if allowed != nil && !slices.Contains(allowed, method) {
			resp.Diagnostics.AddAttributeError(path.Root("method"), "Invalid method",
				fmt.Sprintf("The method of a %s probe is one of %s, in capitals.", kind, strings.Join(allowed, ", ")))
		}
	}
	if kind != "http" || cfg.Method.IsUnknown() {
		return
	}
	if set["request_body"] && !slices.Contains([]string{"POST", "PUT", "PATCH"}, method) {
		resp.Diagnostics.AddAttributeError(path.Root("request_body"), "request_body needs a method that sends one",
			"Set method to POST, PUT or PATCH.")
	}
	if set["request_content_type"] && !set["request_body"] {
		resp.Diagnostics.AddAttributeError(path.Root("request_content_type"), "request_content_type needs request_body",
			"It is the type of the body the probe sends.")
	}
	if set["expect_value"] && !set["json_path"] {
		resp.Diagnostics.AddAttributeError(path.Root("expect_value"), "expect_value needs json_path",
			"On an http probe it is the value at json_path.")
	}
	if method == "HEAD" {
		for _, name := range []string{"body_contains", "body_not_contains", "json_path"} {
			if set[name] {
				resp.Diagnostics.AddAttributeError(path.Root(name), "A HEAD request has no body to check",
					"Use GET, or remove "+name+".")
			}
		}
	}
}

// ModifyPlan fills in the two values the server derives from the rest: the
// method a kind starts with, and the content type of a body sent without one.
func (r *probeResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	var cfg, plan probeModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if cfg.Method.IsNull() && !plan.Kind.IsUnknown() {
		method := ""
		switch plan.Kind.ValueString() {
		case "http":
			method = "GET"
		case "dns":
			method = "A"
		}
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("method"), method)...)
	}
	if cfg.RequestContentType.IsNull() && !plan.RequestBody.IsUnknown() {
		ct := types.StringNull()
		if !plan.RequestBody.IsNull() {
			ct = types.StringValue("application/json")
		}
		resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("request_content_type"), ct)...)
	}
}

func (a *probeAuthModel) body() any {
	if a == nil {
		return nil
	}
	if a.Type.ValueString() == "bearer" {
		return map[string]any{"type": "bearer", "token": a.Token.ValueString()}
	}
	return map[string]any{"type": "basic", "username": a.Username.ValueString(), "password": a.Password.ValueString()}
}

func sameProbeAuth(a, b *probeAuthModel) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Type.Equal(b.Type) && a.Username.Equal(b.Username) && a.Password.Equal(b.Password) && a.Token.Equal(b.Token)
}

// body is the request for a create, or with prior the fields an update
// changes. A value the plan takes away is sent empty, which is how the API
// removes it; on a create it is left out.
func (m *probeModel) body(ctx context.Context, prior *probeModel) map[string]any {
	body := map[string]any{}
	var was map[string]attr.Value
	if prior == nil {
		body["kind"] = m.Kind.ValueString()
	} else {
		was = prior.fields()
	}
	for name, v := range m.fields() {
		if v.IsUnknown() || (prior == nil && v.IsNull()) || (prior != nil && v.Equal(was[name])) {
			continue
		}
		switch v := v.(type) {
		case types.String:
			if prior == nil && v.ValueString() == "" {
				continue
			}
			body[name] = v.ValueString()
		case types.Int64:
			body[name] = v.ValueInt64()
		case types.Bool:
			body[name] = v.ValueBool()
		}
	}
	if prior == nil && !m.RequestHeaders.IsNull() || prior != nil && !m.RequestHeaders.Equal(prior.RequestHeaders) {
		lines := []string{}
		m.RequestHeaders.ElementsAs(ctx, &lines, false)
		body["request_headers"] = lines
	}
	// The server won't carry stored credentials to another host, port or
	// scheme, so a new target takes them along.
	moved := prior != nil && !m.Target.Equal(prior.Target) && m.Auth != nil
	if prior == nil && m.Auth != nil || prior != nil && !sameProbeAuth(m.Auth, prior.Auth) || moved {
		body["auth"] = m.Auth.body()
	}
	return body
}

func text(p *string) types.String {
	if p == nil || *p == "" {
		return types.StringNull()
	}
	return types.StringValue(*p)
}

// probeFromAPI builds the state from an answer. from is the plan or the
// state before it, the only place the auth secret and a domain_expiry choice
// on a target with no domain can come from.
func probeFromAPI(pr *apiProbe, from *probeModel) *probeModel {
	m := &probeModel{
		ID:                 types.StringValue(pr.ID),
		Name:               types.StringValue(pr.Name),
		Kind:               types.StringValue(pr.Kind),
		Target:             types.StringValue(pr.Target),
		Method:             types.StringValue(pr.Method),
		IntervalSecs:       types.Int64Value(pr.IntervalSecs),
		Expect:             types.StringValue(pr.Expect),
		ExpectStatus:       types.Int64PointerValue(pr.ExpectStatus),
		ExpectStatuses:     text(pr.ExpectStatuses),
		ExpectHeader:       text(pr.ExpectHeader),
		BodyContains:       text(pr.BodyContains),
		BodyNotContains:    text(pr.BodyNotContains),
		JSONPath:           text(pr.JSONPath),
		ExpectValue:        text(pr.ExpectValue),
		ExpectSet:          text(pr.ExpectSet),
		IgnoreCase:         types.BoolValue(pr.IgnoreCase),
		Resolver:           text(pr.Resolver),
		Send:               text(pr.Send),
		RequestHeaders:     types.ListNull(types.StringType),
		RequestBody:        text(pr.RequestBody),
		RequestContentType: text(pr.RequestContentType),
		MaxMs:              types.Int64PointerValue(pr.MaxMs),
		AlertSourceID:      text(pr.ChannelID),
		ChannelID:          text(pr.ChannelID),
		PolicyID:           text(pr.PolicyID),
		Host:               text(pr.Host),
		Paused:             types.BoolValue(pr.Paused),
		DomainExpiry:       types.BoolValue(false),
		CreatedAt:          types.StringValue(pr.CreatedAt),
	}
	if len(pr.RequestHeaders) > 0 {
		lines := make([]attr.Value, len(pr.RequestHeaders))
		for i, l := range pr.RequestHeaders {
			lines[i] = types.StringValue(l)
		}
		m.RequestHeaders = types.ListValueMust(types.StringType, lines)
	}
	if pr.DomainExpiry != nil {
		m.DomainExpiry = types.BoolValue(pr.DomainExpiry.Enabled)
	} else if from != nil && !from.DomainExpiry.IsNull() && !from.DomainExpiry.IsUnknown() {
		m.DomainExpiry = from.DomainExpiry
	}
	if pr.Auth != nil {
		m.Auth = &probeAuthModel{Type: types.StringValue(pr.Auth.Type), Username: text(&pr.Auth.Username)}
		if from != nil && from.Auth != nil && from.Auth.Type.Equal(m.Auth.Type) {
			m.Auth.Password, m.Auth.Token = from.Auth.Password, from.Auth.Token
		}
	}
	return m
}

func duplicateProbe(err error) string {
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.Status == 409 && apiErr.Code == "duplicate_probe" {
		return apiErr.ExistingID
	}
	return ""
}

func (r *probeResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan probeModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	pr, err := r.client.CreateProbe(ctx, plan.body(ctx, nil))
	if id := duplicateProbe(err); id != "" {
		resp.Diagnostics.AddError("A probe already checks this",
			fmt.Sprintf("Probe %s checks exactly the same thing, and Mast keeps one probe per check. "+
				"Import it into this resource instead of making a second one:\n\n"+
				"  terraform import tissue_mast_probe.<name> %s", id, id))
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Creating the probe failed", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, probeFromAPI(pr, &plan))...)
}

func (r *probeResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state probeModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	pr, err := r.client.GetProbe(ctx, state.ID.ValueString())
	if isNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Reading the probe failed", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, probeFromAPI(pr, &state))...)
}

func (r *probeResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state probeModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	pr, err := r.client.UpdateProbe(ctx, state.ID.ValueString(), plan.body(ctx, &state))
	if id := duplicateProbe(err); id != "" {
		resp.Diagnostics.AddError("A probe already checks this",
			fmt.Sprintf("With this change the probe would check exactly what %s checks, and Mast keeps one probe "+
				"per check. Change one of the two, or remove one.", id))
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Updating the probe failed", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, probeFromAPI(pr, &plan))...)
}

func (r *probeResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var id types.String
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("id"), &id)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteProbe(ctx, id.ValueString()); err != nil && !isNotFound(err) {
		resp.Diagnostics.AddError("Deleting the probe failed", err.Error())
	}
}

func (r *probeResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	pr, err := r.client.FindProbe(ctx, req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Can't import that probe", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), pr.ID)...)
}

var (
	_ resource.ResourceWithImportState    = &probeResource{}
	_ resource.ResourceWithModifyPlan     = &probeResource{}
	_ resource.ResourceWithValidateConfig = &probeResource{}
)
