package provider

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

const policyAfterMax = 7 * 86400

type policyResource struct {
	client *Client
}

func newPolicyResource() resource.Resource { return &policyResource{} }

type policyModel struct {
	ID              types.String `tfsdk:"id"`
	Name            types.String `tfsdk:"name"`
	Steps           []stepModel  `tfsdk:"steps"`
	RepeatEverySecs types.Int64  `tfsdk:"repeat_every_secs"`
	RepeatCount     types.Int64  `tfsdk:"repeat_count"`
	CreatedAt       types.String `tfsdk:"created_at"`
}

type stepModel struct {
	AfterSecs  types.Int64  `tfsdk:"after_secs"`
	Target     types.String `tfsdk:"target"`
	RotationID types.String `tfsdk:"rotation_id"`
	UserID     types.String `tfsdk:"user_id"`
	ChannelID  types.String `tfsdk:"channel_id"`
	Priority   types.String `tfsdk:"priority"`
}

func (r *policyResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_mast_policy"
}

func (r *policyResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "An escalation policy: who a page goes to, and who it goes to next while nobody " +
			"acknowledges it. Attach it to a channel or vital with `policy_id`.",
		Attributes: map[string]schema.Attribute{
			"id": computedString("The policy id, `po_...`."),
			"name": schema.StringAttribute{
				MarkdownDescription: "The policy's name.",
				Required:            true,
				Validators:          []validator.String{stringvalidator.UTF8LengthBetween(1, 80), trimmedValidator{}},
			},
			"steps": schema.ListNestedAttribute{
				MarkdownDescription: "The steps, in order. The first fires at once.",
				Required:            true,
				Validators:          []validator.List{listvalidator.SizeAtLeast(1)},
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"after_secs": schema.Int64Attribute{
							MarkdownDescription: "Seconds after the step before, up to a week. Ignored on the first step.",
							Optional:            true,
							Computed:            true,
							Default:             int64default.StaticInt64(0),
							Validators:          []validator.Int64{int64validator.Between(0, policyAfterMax)},
						},
						"target": schema.StringAttribute{
							MarkdownDescription: "Who the step pages: `oncall` (whoever holds `rotation_id`), `next` " +
								"(whoever has the rotation's next shift), `everyone`, `user` or `channel`.",
							Required: true,
							Validators: []validator.String{
								stringvalidator.OneOf("oncall", "next", "everyone", "user", "channel"),
							},
						},
						"rotation_id": schema.StringAttribute{
							MarkdownDescription: "The rotation an `oncall` or `next` step reads. Without one the step pages everyone.",
							Optional:            true,
							Validators: []validator.String{stringvalidator.RegexMatches(regexp.MustCompile(`^sc_`),
								"must be a rotation id, sc_...; use data.tissue_mast_rotation to look one up by name")},
						},
						"user_id": schema.StringAttribute{
							MarkdownDescription: "The person a `user` step pages.",
							Optional:            true,
						},
						"channel_id": schema.StringAttribute{
							MarkdownDescription: "The channel a `channel` step sends to, `mc_...`.",
							Optional:            true,
							Validators: []validator.String{
								stringvalidator.RegexMatches(regexp.MustCompile(`^mc_`), "must be a channel id, mc_..."),
							},
						},
						"priority": schema.StringAttribute{
							MarkdownDescription: "`page`, `loud` or `quiet`.",
							Required:            true,
							Validators:          []validator.String{stringvalidator.OneOf("page", "loud", "quiet")},
						},
					},
				},
			},
			"repeat_every_secs": schema.Int64Attribute{
				MarkdownDescription: "Start the steps over this many seconds after the last one, 60 up to a week. " +
					"Leave it out for no repeat.",
				Optional:   true,
				Validators: []validator.Int64{int64validator.Between(60, policyAfterMax)},
			},
			"repeat_count": schema.Int64Attribute{
				MarkdownDescription: "How many times the steps repeat, 1 to 10. Defaults to 3 when `repeat_every_secs` is set.",
				Optional:            true,
				Computed:            true,
				Validators:          []validator.Int64{int64validator.Between(1, 10)},
			},
			"created_at": computedString("When the policy was made."),
		},
	}
}

func (r *policyResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if c, ok := req.ProviderData.(*Client); ok {
		r.client = c
	}
}

func given(v types.String) bool { return !v.IsNull() }

func (r *policyResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg policyModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if cfg.RepeatEverySecs.IsNull() && !cfg.RepeatCount.IsNull() {
		resp.Diagnostics.AddAttributeError(path.Root("repeat_count"), "repeat_count needs repeat_every_secs",
			"Set repeat_every_secs too, or remove repeat_count.")
	}
	for i, s := range cfg.Steps {
		if s.Target.IsUnknown() {
			continue
		}
		at := path.Root("steps").AtListIndex(i)
		allowed := map[string]bool{}
		switch t := s.Target.ValueString(); t {
		case "oncall", "next":
			allowed["rotation_id"] = true
		case "user":
			allowed["user_id"] = true
			if !given(s.UserID) {
				resp.Diagnostics.AddAttributeError(at.AtName("user_id"), "A user step needs user_id", "Name the person it pages.")
			}
		case "channel":
			allowed["channel_id"] = true
			if !given(s.ChannelID) {
				resp.Diagnostics.AddAttributeError(at.AtName("channel_id"), "A channel step needs channel_id", "Name the channel it sends to.")
			}
		}
		for name, v := range map[string]types.String{"rotation_id": s.RotationID, "user_id": s.UserID, "channel_id": s.ChannelID} {
			if given(v) && !allowed[name] {
				resp.Diagnostics.AddAttributeError(at.AtName(name), "Not used by this target",
					fmt.Sprintf("A %s step has no %s.", s.Target.ValueString(), name))
			}
		}
	}
}

// ModifyPlan works out repeat_count the way the server will: 3 when a repeat
// is turned on without one, the stored count while the repeat stays on, and
// nothing when it is off.
func (r *policyResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() {
		return
	}
	var every, configCount, stateCount types.Int64
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("repeat_every_secs"), &every)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("repeat_count"), &configCount)...)
	if !req.State.Raw.IsNull() {
		resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("repeat_count"), &stateCount)...)
	}
	if resp.Diagnostics.HasError() || !configCount.IsNull() {
		return
	}
	var count types.Int64
	switch {
	case every.IsUnknown():
		count = types.Int64Unknown()
	case every.IsNull():
		count = types.Int64Null()
	case !stateCount.IsNull() && !stateCount.IsUnknown():
		count = stateCount
	default:
		count = types.Int64Value(3)
	}
	resp.Diagnostics.Append(resp.Plan.SetAttribute(ctx, path.Root("repeat_count"), count)...)
}

func (m *policyModel) body() map[string]any {
	steps := make([]apiStep, len(m.Steps))
	for i, s := range m.Steps {
		step := apiStep{AfterSecs: s.AfterSecs.ValueInt64(), Priority: s.Priority.ValueString()}
		step.Target.Type = s.Target.ValueString()
		for _, v := range []types.String{s.RotationID, s.UserID, s.ChannelID} {
			if given(v) {
				id := v.ValueString()
				step.Target.ID = &id
			}
		}
		steps[i] = step
	}
	body := map[string]any{"name": m.Name.ValueString(), "steps": steps}
	if m.RepeatEverySecs.IsNull() {
		body["repeat_every_secs"] = nil
	} else {
		body["repeat_every_secs"] = m.RepeatEverySecs.ValueInt64()
	}
	if !m.RepeatCount.IsNull() && !m.RepeatCount.IsUnknown() {
		body["repeat_count"] = m.RepeatCount.ValueInt64()
	}
	return body
}

func policyFromAPI(po *apiPolicy) *policyModel {
	m := &policyModel{
		ID:              types.StringValue(po.ID),
		Name:            types.StringValue(po.Name),
		RepeatEverySecs: types.Int64PointerValue(po.RepeatEverySecs),
		RepeatCount:     types.Int64PointerValue(po.RepeatCount),
		CreatedAt:       types.StringValue(po.CreatedAt),
	}
	for _, s := range po.Steps {
		step := stepModel{
			AfterSecs:  types.Int64Value(s.AfterSecs),
			Target:     types.StringValue(s.Target.Type),
			Priority:   types.StringValue(s.Priority),
			RotationID: types.StringNull(),
			UserID:     types.StringNull(),
			ChannelID:  types.StringNull(),
		}
		if s.Target.ID != nil && *s.Target.ID != "" {
			id := types.StringValue(*s.Target.ID)
			switch s.Target.Type {
			case "oncall", "next":
				step.RotationID = id
			case "user":
				step.UserID = id
			case "channel":
				step.ChannelID = id
			}
		}
		m.Steps = append(m.Steps, step)
	}
	return m
}

func (r *policyResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan policyModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body := plan.body()
	if body["repeat_every_secs"] == nil {
		delete(body, "repeat_every_secs")
	}
	po, err := r.client.CreatePolicy(ctx, body)
	if err != nil {
		resp.Diagnostics.AddError("Creating the policy failed", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, policyFromAPI(po))...)
}

func (r *policyResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var id types.String
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("id"), &id)...)
	if resp.Diagnostics.HasError() {
		return
	}
	po, err := r.client.GetPolicy(ctx, id.ValueString())
	if isNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Reading the policy failed", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, policyFromAPI(po))...)
}

func (r *policyResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan policyModel
	var id types.String
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("id"), &id)...)
	if resp.Diagnostics.HasError() {
		return
	}
	po, err := r.client.UpdatePolicy(ctx, id.ValueString(), plan.body())
	if err != nil {
		resp.Diagnostics.AddError("Updating the policy failed", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, policyFromAPI(po))...)
}

func (r *policyResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var id types.String
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("id"), &id)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if err := r.client.DeletePolicy(ctx, id.ValueString()); err != nil && !isNotFound(err) {
		resp.Diagnostics.AddError("Deleting the policy failed", err.Error())
	}
}

func (r *policyResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	id := req.ID
	if !strings.HasPrefix(id, "po_") {
		list, err := r.client.Policies(ctx)
		if err != nil {
			resp.Diagnostics.AddError("Listing policies failed", err.Error())
			return
		}
		var ids []string
		for _, po := range list {
			if po.Name == req.ID {
				ids = append(ids, po.ID)
			}
		}
		if len(ids) != 1 {
			resp.Diagnostics.AddError("Can't import by that name",
				fmt.Sprintf("%d policies are named %q %v. Import by id instead.", len(ids), req.ID, ids))
			return
		}
		id = ids[0]
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), id)...)
}

var (
	_ resource.ResourceWithImportState    = &policyResource{}
	_ resource.ResourceWithModifyPlan     = &policyResource{}
	_ resource.ResourceWithValidateConfig = &policyResource{}
)
