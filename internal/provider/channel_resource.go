package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/boolplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/tfsdk"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Channels and vitals are one API object told apart by kind, and so one
// implementation here. They are separate resource types because a vital has
// a dozen settings a channel doesn't, and the kind can't change after create.
type channelResource struct {
	kind   string
	client *Client
}

func newChannelResource() resource.Resource { return &channelResource{kind: "channel"} }
func newVitalResource() resource.Resource   { return &channelResource{kind: "vital"} }

type channelModel struct {
	ID                types.String `tfsdk:"id"`
	Name              types.String `tfsdk:"name"`
	URL               types.String `tfsdk:"url"`
	DefaultPriority   types.String `tfsdk:"default_priority"`
	MaxPriority       types.String `tfsdk:"max_priority"`
	Sound             types.String `tfsdk:"sound"`
	QuietStart        types.Int64  `tfsdk:"quiet_start"`
	QuietEnd          types.Int64  `tfsdk:"quiet_end"`
	QuietTZ           types.String `tfsdk:"quiet_tz"`
	DedupeWindowSecs  types.Int64  `tfsdk:"dedupe_window_secs"`
	StormThreshold    types.Int64  `tfsdk:"storm_threshold"`
	StormWindowSecs   types.Int64  `tfsdk:"storm_window_secs"`
	RepeatSecs        types.Int64  `tfsdk:"repeat_secs"`
	ExpireSecs        types.Int64  `tfsdk:"expire_secs"`
	EscalateAfterSecs types.Int64  `tfsdk:"escalate_after_secs"`
	AckTimeoutMin     types.Int64  `tfsdk:"ack_timeout_min"`
	CanaryURL         types.String `tfsdk:"canary_url"`
	PolicyID          types.String `tfsdk:"policy_id"`
	CreatedAt         types.String `tfsdk:"created_at"`
	Archived          types.Bool   `tfsdk:"archived"`
}

type vitalModel struct {
	channelModel

	PeriodSecs      types.Int64  `tfsdk:"period_secs"`
	GraceSecs       types.Int64  `tfsdk:"grace_secs"`
	Schedule        types.String `tfsdk:"schedule"`
	ScheduleTZ      types.String `tfsdk:"schedule_tz"`
	MaxDurationSecs types.Int64  `tfsdk:"max_duration_secs"`
	VitalPriority   types.String `tfsdk:"vital_priority"`
	FailThreshold   types.Int64  `tfsdk:"fail_threshold"`
	SmartEarlyWarn  types.Bool   `tfsdk:"smart_early_warn"`
	Host            types.String `tfsdk:"host"`
	Paused          types.Bool   `tfsdk:"paused"`
}

// The fields sent as they are, keyed by their name in both the schema and
// the API. max_priority is left out: it is sent once, on create.
func (m *vitalModel) ints() map[string]*types.Int64 {
	return map[string]*types.Int64{
		"quiet_start":         &m.QuietStart,
		"quiet_end":           &m.QuietEnd,
		"dedupe_window_secs":  &m.DedupeWindowSecs,
		"storm_threshold":     &m.StormThreshold,
		"storm_window_secs":   &m.StormWindowSecs,
		"repeat_secs":         &m.RepeatSecs,
		"expire_secs":         &m.ExpireSecs,
		"escalate_after_secs": &m.EscalateAfterSecs,
		"ack_timeout_min":     &m.AckTimeoutMin,
		"period_secs":         &m.PeriodSecs,
		"grace_secs":          &m.GraceSecs,
		"max_duration_secs":   &m.MaxDurationSecs,
		"fail_threshold":      &m.FailThreshold,
	}
}

func (m *vitalModel) strings() map[string]*types.String {
	return map[string]*types.String{
		"name":             &m.Name,
		"default_priority": &m.DefaultPriority,
		"sound":            &m.Sound,
		"quiet_tz":         &m.QuietTZ,
		"canary_url":       &m.CanaryURL,
		"policy_id":        &m.PolicyID,
		"schedule":         &m.Schedule,
		"schedule_tz":      &m.ScheduleTZ,
		"vital_priority":   &m.VitalPriority,
		"host":             &m.Host,
	}
}

// Not accepted by POST; Create sends them in a PATCH straight after.
var patchOnly = map[string]bool{"ack_timeout_min": true, "canary_url": true, "policy_id": true}

func (r *channelResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_mast_" + r.kind
}

func optionalInt(desc string, v ...validator.Int64) schema.Int64Attribute {
	return schema.Int64Attribute{
		MarkdownDescription: desc,
		Optional:            true,
		Computed:            true,
		Validators:          v,
		PlanModifiers:       []planmodifier.Int64{int64planmodifier.UseStateForUnknown()},
	}
}

func optionalString(desc string, v ...validator.String) schema.StringAttribute {
	return schema.StringAttribute{
		MarkdownDescription: desc,
		Optional:            true,
		Computed:            true,
		Validators:          v,
		PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
	}
}

func computedString(desc string) schema.StringAttribute {
	return schema.StringAttribute{
		MarkdownDescription: desc,
		Computed:            true,
		PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
	}
}

func secs(max int64) validator.Int64 { return int64validator.Between(0, max) }

var priorities = []string{"quiet", "normal", "loud", "page"}

func (r *channelResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	attrs := map[string]schema.Attribute{
		"id": computedString("The " + r.kind + " id, `mc_...`."),
		"name": schema.StringAttribute{
			MarkdownDescription: "The name shown in the app and on every notification. Lowercase letters, digits, " +
				"`.`, `_`, `-` and single spaces, at most 32 characters. Renaming keeps the URL.",
			Required:   true,
			Validators: []validator.String{channelNameValidator{}},
		},
		"url": schema.StringAttribute{
			MarkdownDescription: "The ingest URL. It carries the key, so anyone holding it can send to this " + r.kind + ".",
			Computed:            true,
			Sensitive:           true,
			PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
		},
		"default_priority": optionalString("Priority of a send that doesn't name one: `quiet`, `normal`, `loud` or `page`. "+
			"The server's default is `normal`.", stringvalidator.OneOf(priorities...)),
		"max_priority": schema.StringAttribute{
			MarkdownDescription: "The highest priority a send can ask for. A new " + r.kind + " can start at `quiet`, " +
				"`normal` or `loud` (the default). Raising it to `page` is done in the Mast app, on the owner's phone, " +
				"and after create the value can only be changed there. Leave it unset to follow the app, or set it to " +
				"what the app shows.",
			Optional:   true,
			Computed:   true,
			Validators: []validator.String{stringvalidator.OneOf(priorities...)},
			PlanModifiers: []planmodifier.String{
				stringplanmodifier.UseStateForUnknown(),
				maxPriorityModifier{kind: r.kind},
			},
		},
		"sound": optionalString("The sound a push plays: `default`, `bleep`, `chirp`, `klaxon`, `pager` or `rising`. "+
			"Empty means whatever the priority plays.",
			stringvalidator.OneOf("", "default", "bleep", "chirp", "klaxon", "pager", "rising")),
		"quiet_start": optionalInt("Start of a daily quiet window, in minutes past midnight, or `-1` for none.",
			int64validator.Between(-1, 1439)),
		"quiet_end": optionalInt("End of the quiet window, in minutes past midnight, or `-1` for none.",
			int64validator.Between(-1, 1439)),
		"quiet_tz": optionalString("Time zone the quiet window is read in: an IANA name such as `Europe/Berlin`, "+
			"or an offset such as `+0530`. Empty is UTC.", zoneValidator{}),
		"dedupe_window_secs":  optionalInt("Repeats of one message inside this many seconds fold into one. Default 300.", secs(86400)),
		"storm_threshold":     optionalInt("Sends inside the storm window past which Mast holds the rest. Default 20.", secs(10000)),
		"storm_window_secs":   optionalInt("Length of the storm window. Default 300.", secs(86400)),
		"repeat_secs":         optionalInt("How often an unacknowledged page repeats. Default 120.", secs(86400)),
		"expire_secs":         optionalInt("How long an unacknowledged page keeps repeating. Default 3600.", secs(604800)),
		"escalate_after_secs": optionalInt("Seconds before an unacknowledged page escalates. `0` turns it off.", secs(86400)),
		"ack_timeout_min": optionalInt("Minutes an acknowledged incident stays quiet before paging again. `0` never re-pages.",
			int64validator.Between(0, 10080)),
		"canary_url": optionalString("An http or https URL Mast fetches to check the sender is still alive. Empty turns it off.",
			trimmedValidator{}, stringvalidator.LengthAtMost(2048)),
		"policy_id": optionalString("The escalation policy pages on this "+r.kind+" follow, `po_...`. Set it to `\"\"` "+
			"to detach one; leaving the argument out keeps whatever the app has.", trimmedValidator{}),
		"created_at": computedString("When the " + r.kind + " was made."),
		"archived": schema.BoolAttribute{
			MarkdownDescription: "Whether the " + r.kind + " is archived. An archived " + r.kind + " takes no sends " +
				"and its URL answers 404 until it's restored. Leave it unset to follow the app, where people archive " +
				"and restore; set it to `false` to restore one archived there. Restoring counts against the plan's " +
				"channel limit again.",
			Optional:      true,
			Computed:      true,
			PlanModifiers: []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
		},
	}

	desc := "A Mast channel: a URL that turns what is sent to it into notifications on the phones of the account. " +
		"Channels with `kind = \"vital\"` in the API are managed with `tissue_mast_vital`."
	if r.kind == "vital" {
		desc = "A Mast vital: a channel that expects a beat on a schedule and pages when one is late. " +
			"Send a beat by requesting its `url`."
		attrs["period_secs"] = optionalInt("Seconds between beats for an interval vital. `0` when the vital runs on a `schedule`.",
			secs(2592000))
		attrs["grace_secs"] = optionalInt("How late a beat can be before the vital pages. A `schedule` needs it above 0.",
			secs(2592000))
		attrs["schedule"] = optionalString("A five-field crontab for when the watched job starts, such as `0 3 * * *`.",
			trimmedValidator{})
		attrs["schedule_tz"] = optionalString("Time zone the schedule is read in, as for `quiet_tz`. Empty is UTC.",
			zoneValidator{})
		attrs["max_duration_secs"] = optionalInt("Longest a run between `/start` and its beat may take. `0` is no limit.",
			secs(2592000))
		attrs["vital_priority"] = optionalString("Priority of the page a late vital sends. Default `loud`.",
			stringvalidator.OneOf(priorities...))
		attrs["fail_threshold"] = optionalInt("Missed beats in a row before it pages. Default 1.", int64validator.Between(1, 100))
		attrs["smart_early_warn"] = schema.BoolAttribute{
			MarkdownDescription: "Warn quietly when beats start arriving later than usual, before the vital is late.",
			Optional:            true,
			Computed:            true,
			PlanModifiers:       []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
		}
		attrs["host"] = optionalString("The machine the job runs on, shown with the page. Letters, digits, `.`, `_` and `-`.",
			trimmedValidator{}, stringvalidator.LengthAtMost(64))
		attrs["paused"] = schema.BoolAttribute{
			MarkdownDescription: "Takes the vital off watch. Leave it unset if people pause it from the app.",
			Optional:            true,
			Computed:            true,
			PlanModifiers:       []planmodifier.Bool{boolplanmodifier.UseStateForUnknown()},
		}
	}
	resp.Schema = schema.Schema{MarkdownDescription: desc, Attributes: attrs}
}

// maxPriorityModifier holds the line the server holds: a token can choose a
// new channel's ceiling up to loud, and can't change it afterwards.
type maxPriorityModifier struct{ kind string }

func (maxPriorityModifier) Description(context.Context) string {
	return "max_priority is chosen at create and changed only in the app"
}

func (m maxPriorityModifier) MarkdownDescription(ctx context.Context) string {
	return m.Description(ctx)
}

func (m maxPriorityModifier) PlanModifyString(_ context.Context, req planmodifier.StringRequest, resp *planmodifier.StringResponse) {
	if req.ConfigValue.IsNull() || req.ConfigValue.IsUnknown() {
		return
	}
	want := req.ConfigValue.ValueString()
	if req.State.Raw.IsNull() {
		if want == "page" {
			resp.Diagnostics.AddAttributeError(req.Path, "max_priority can't start at page",
				fmt.Sprintf("A new %s's max_priority is loud at most. Create it at loud, then raise it to page "+
					"in the Mast app on the owner's phone and set max_priority = \"page\" here to match.", m.kind))
		}
		return
	}
	if req.StateValue.IsNull() || req.StateValue.IsUnknown() {
		return
	}
	if have := req.StateValue.ValueString(); have != want {
		resp.Diagnostics.AddAttributeError(req.Path, "max_priority is changed in the app",
			fmt.Sprintf("This %s's max_priority is %q and the configuration says %q. It can only be changed in the "+
				"Mast app, on the owner's phone. Change it there, or set max_priority = %q here, or remove the argument.",
				m.kind, have, want, have))
	}
}

func (r *channelResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}
	client, ok := req.ProviderData.(*Client)
	if !ok {
		resp.Diagnostics.AddError("Unexpected provider data", fmt.Sprintf("got %T", req.ProviderData))
		return
	}
	r.client = client
}

type getter interface {
	Get(context.Context, any) diag.Diagnostics
}

func (r *channelResource) get(ctx context.Context, src getter) (*vitalModel, diag.Diagnostics) {
	if r.kind == "vital" {
		var m vitalModel
		d := src.Get(ctx, &m)
		return &m, d
	}
	var m channelModel
	d := src.Get(ctx, &m)
	return &vitalModel{channelModel: m}, d
}

func (r *channelResource) set(ctx context.Context, dst *tfsdk.State, m *vitalModel) diag.Diagnostics {
	if r.kind == "vital" {
		return dst.Set(ctx, m)
	}
	return dst.Set(ctx, &m.channelModel)
}

func (r *channelResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if r.kind != "vital" || req.Plan.Raw.IsNull() {
		return
	}
	var schedule types.String
	var grace types.Int64
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("schedule"), &schedule)...)
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("grace_secs"), &grace)...)
	if resp.Diagnostics.HasError() || schedule.IsUnknown() || schedule.ValueString() == "" {
		return
	}
	// On create an unset grace is the server's 0.
	zero := grace.IsUnknown() && req.State.Raw.IsNull()
	if zero || (!grace.IsUnknown() && grace.ValueInt64() <= 0) {
		resp.Diagnostics.AddAttributeError(path.Root("grace_secs"), "A schedule needs grace_secs",
			"A crontab says when a run starts, not when it reports, so set grace_secs to how long Mast should "+
				"wait for the beat before paging.")
	}
}

func (r *channelResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	plan, diags := r.get(ctx, req.Plan)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	body := map[string]any{"kind": r.kind}
	later := map[string]any{}
	pick := func(key string, v attr.Value, value any) {
		if v.IsNull() || v.IsUnknown() {
			return
		}
		if patchOnly[key] {
			later[key] = value
		} else {
			body[key] = value
		}
	}
	for key, v := range plan.ints() {
		pick(key, *v, v.ValueInt64())
	}
	for key, v := range plan.strings() {
		pick(key, *v, v.ValueString())
	}
	pick("max_priority", plan.MaxPriority, plan.MaxPriority.ValueString())
	pick("smart_early_warn", plan.SmartEarlyWarn, plan.SmartEarlyWarn.ValueBool())
	if !plan.Paused.IsNull() && !plan.Paused.IsUnknown() && plan.Paused.ValueBool() {
		later["vital_state"] = "paused"
	}
	if !plan.Archived.IsNull() && !plan.Archived.IsUnknown() && plan.Archived.ValueBool() {
		later["archived"] = true
	}
	if v, ok := later["policy_id"]; ok && v == "" {
		delete(later, "policy_id")
	}

	created, err := r.client.CreateChannel(ctx, body)
	if err != nil {
		resp.Diagnostics.AddError("Creating the "+r.kind+" failed", err.Error())
		return
	}

	if len(later) > 0 {
		if _, err := r.client.UpdateChannel(ctx, created.ID, later); err != nil {
			// The channel exists; save it so Terraform taints it rather than
			// losing track of it.
			state := modelFromAPI(created, plan)
			state.PolicyID = types.StringValue("")
			resp.Diagnostics.Append(r.set(ctx, &resp.State, state)...)
			resp.Diagnostics.AddError("Setting up the "+r.kind+" failed", err.Error())
			return
		}
	}

	state, err := r.read(ctx, created.ID, created.URL, plan)
	if err != nil {
		resp.Diagnostics.AddError("Reading the "+r.kind+" back failed", err.Error())
		return
	}
	if state == nil {
		resp.Diagnostics.AddError("The "+r.kind+" vanished", "It was created as "+created.ID+" and then not found.")
		return
	}
	resp.Diagnostics.Append(r.set(ctx, &resp.State, state)...)
}

// read returns nil when the channel is gone.
func (r *channelResource) read(ctx context.Context, id string, knownURL *string, prior *vitalModel) (*vitalModel, error) {
	list, err := r.client.Channels(ctx)
	if err != nil {
		return nil, err
	}
	var ch *apiChannel
	for i := range list {
		if list[i].ID == id {
			ch = &list[i]
			break
		}
	}
	if ch == nil {
		// Archived channels drop out of the list but still answer by id,
		// without their URL.
		ch, err = r.client.GetChannel(ctx, id)
		if isNotFound(err) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
	}
	if ch.URL == nil {
		ch.URL = knownURL
	}

	m := modelFromAPI(ch, prior)
	policy, err := r.client.PolicyOf(ctx, ch.ID)
	if err != nil {
		return nil, err
	}
	m.PolicyID = types.StringValue(policy)
	return m, nil
}

func modelFromAPI(ch *apiChannel, prior *vitalModel) *vitalModel {
	m := &vitalModel{}
	m.ID = types.StringValue(ch.ID)
	m.Name = types.StringValue(ch.Name)
	switch {
	case ch.URL != nil:
		m.URL = types.StringValue(*ch.URL)
	case prior != nil && !prior.URL.IsUnknown():
		m.URL = prior.URL
	default:
		m.URL = types.StringNull()
	}
	m.DefaultPriority = types.StringValue(ch.DefaultPriority)
	m.MaxPriority = types.StringValue(ch.MaxPriority)
	m.Sound = types.StringValue(strings.TrimSuffix(str(ch.Sound), ".caf"))
	m.QuietStart = types.Int64Value(ch.QuietStart)
	m.QuietEnd = types.Int64Value(ch.QuietEnd)
	m.QuietTZ = types.StringValue(str(ch.QuietTZ))
	m.DedupeWindowSecs = types.Int64Value(ch.DedupeWindowSecs)
	m.StormThreshold = types.Int64Value(ch.StormThreshold)
	m.StormWindowSecs = types.Int64Value(ch.StormWindowSecs)
	m.RepeatSecs = types.Int64Value(ch.RepeatSecs)
	m.ExpireSecs = types.Int64Value(ch.ExpireSecs)
	m.EscalateAfterSecs = types.Int64Value(ch.EscalateAfterSecs)
	m.AckTimeoutMin = types.Int64Value(ch.AckTimeoutMin)
	m.CanaryURL = types.StringValue(str(ch.CanaryURL))
	m.CreatedAt = types.StringValue(ch.CreatedAt)
	m.Archived = types.BoolValue(ch.ArchivedAt != nil && *ch.ArchivedAt != "")

	m.PeriodSecs = types.Int64Value(ch.PeriodSecs)
	m.GraceSecs = types.Int64Value(ch.GraceSecs)
	m.Schedule = types.StringValue(str(ch.Schedule))
	m.ScheduleTZ = types.StringValue(str(ch.ScheduleTZ))
	m.MaxDurationSecs = types.Int64Value(ch.MaxDurationSecs)
	m.VitalPriority = types.StringValue(ch.VitalPriority)
	m.FailThreshold = types.Int64Value(ch.FailThreshold)
	m.SmartEarlyWarn = types.BoolValue(ch.SmartEarlyWarn)
	m.Host = types.StringValue(str(ch.Host))
	m.Paused = types.BoolValue(ch.VitalState == "paused")
	return m
}

func (r *channelResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	prior, diags := r.get(ctx, req.State)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	state, err := r.read(ctx, prior.ID.ValueString(), nil, prior)
	if err != nil {
		resp.Diagnostics.AddError("Reading the "+r.kind+" failed", err.Error())
		return
	}
	if state == nil {
		resp.State.RemoveResource(ctx)
		return
	}
	resp.Diagnostics.Append(r.set(ctx, &resp.State, state)...)
}

func (r *channelResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	plan, diags := r.get(ctx, req.Plan)
	resp.Diagnostics.Append(diags...)
	prior, diags := r.get(ctx, req.State)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	body := map[string]any{}
	priorInts, priorStrings := prior.ints(), prior.strings()
	for key, v := range plan.ints() {
		if !v.IsUnknown() && !v.IsNull() && !v.Equal(*priorInts[key]) {
			body[key] = v.ValueInt64()
		}
	}
	for key, v := range plan.strings() {
		if !v.IsUnknown() && !v.IsNull() && !v.Equal(*priorStrings[key]) {
			body[key] = v.ValueString()
		}
	}
	if b := plan.SmartEarlyWarn; !b.IsUnknown() && !b.IsNull() && !b.Equal(prior.SmartEarlyWarn) {
		body["smart_early_warn"] = b.ValueBool()
	}
	if b := plan.Paused; !b.IsUnknown() && !b.IsNull() && !b.Equal(prior.Paused) {
		body["vital_state"] = map[bool]string{true: "paused", false: "alive"}[b.ValueBool()]
	}
	if b := plan.Archived; !b.IsUnknown() && !b.IsNull() && !b.Equal(prior.Archived) {
		body["archived"] = b.ValueBool()
	}

	var knownURL *string
	if len(body) > 0 {
		updated, err := r.client.UpdateChannel(ctx, prior.ID.ValueString(), body)
		if err != nil {
			resp.Diagnostics.AddError("Updating the "+r.kind+" failed", err.Error())
			return
		}
		knownURL = updated.URL
	}

	state, err := r.read(ctx, prior.ID.ValueString(), knownURL, prior)
	if err != nil {
		resp.Diagnostics.AddError("Reading the "+r.kind+" back failed", err.Error())
		return
	}
	if state == nil {
		resp.Diagnostics.AddError("The "+r.kind+" vanished", prior.ID.ValueString()+" was not found after the update.")
		return
	}
	resp.Diagnostics.Append(r.set(ctx, &resp.State, state)...)
}

func (r *channelResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var id types.String
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("id"), &id)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeleteChannel(ctx, id.ValueString()); err != nil && !isNotFound(err) {
		resp.Diagnostics.AddError("Deleting the "+r.kind+" failed", err.Error())
	}
}

// ImportState takes an id or a name; the API answers to both.
func (r *channelResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	ch, err := r.client.GetChannel(ctx, req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Finding the "+r.kind+" failed", err.Error())
		return
	}
	if ch.Kind != r.kind {
		other := "tissue_mast_" + ch.Kind
		resp.Diagnostics.AddError("Wrong resource type",
			fmt.Sprintf("%s (%s) is a %s. Import it as %s.", ch.Name, ch.ID, ch.Kind, other))
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), ch.ID)...)
}

var (
	_ resource.ResourceWithImportState = &channelResource{}
	_ resource.ResourceWithModifyPlan  = &channelResource{}
	_ resource.ResourceWithConfigure   = &channelResource{}
)
