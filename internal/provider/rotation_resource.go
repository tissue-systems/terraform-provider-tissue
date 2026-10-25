package provider

import (
	"context"
	"fmt"
	"regexp"

	"github.com/hashicorp/terraform-plugin-framework-validators/int64validator"
	"github.com/hashicorp/terraform-plugin-framework-validators/listvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/listplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// A rotation is what the API calls a schedule. Its overrides are left alone
// here and managed with tissue_mast_override, one resource per override.
type rotationResource struct {
	client *Client
}

func newRotationResource() resource.Resource { return &rotationResource{} }

type rotationModel struct {
	ID          types.String `tfsdk:"id"`
	Name        types.String `tfsdk:"name"`
	Handoff     types.String `tfsdk:"handoff"`
	HandoffDay  types.Int64  `tfsdk:"handoff_day"`
	HandoffTime types.String `tfsdk:"handoff_time"`
	LengthHours types.Int64  `tfsdk:"length_hours"`
	TZ          types.String `tfsdk:"tz"`
	Order       types.List   `tfsdk:"order"`
	CreatedAt   types.String `tfsdk:"created_at"`
}

var hhmm = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)

func (r *rotationResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_mast_rotation"
}

func (r *rotationResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "An on-call rotation (a schedule, in the API). People in `order` take turns, " +
			"handing off at `handoff_time` in `tz`. Point a policy step at it to page whoever is on call.",
		Attributes: map[string]schema.Attribute{
			"id": computedString("The rotation id, `sc_...`."),
			"name": schema.StringAttribute{
				MarkdownDescription: "The rotation's name.",
				Required:            true,
				Validators:          []validator.String{stringvalidator.UTF8LengthBetween(1, 80), trimmedValidator{}},
			},
			"handoff": optionalString("How often the rotation hands off: `daily`, `weekly` (the default), `biweekly`, "+
				"or `custom` with `length_hours`.", stringvalidator.OneOf("daily", "weekly", "biweekly", "custom")),
			"handoff_day": optionalInt("Day of a weekly or biweekly handoff, 0 for Monday to 6 for Sunday. Default 0.",
				int64validator.Between(0, 6)),
			"handoff_time": optionalString("Time of the handoff, `HH:MM` on a 24-hour clock. Default `09:00`.",
				stringvalidator.RegexMatches(hhmm, "must be HH:MM, 24-hour")),
			"length_hours": optionalInt("Shift length for a `custom` handoff, 1 to 720. It is 0 for the other handoffs.",
				int64validator.Between(0, 720)),
			"tz": optionalString("IANA time zone the handoffs and overrides are read in. Default `UTC`.",
				stringvalidator.LengthAtLeast(1), trimmedValidator{}),
			"order": schema.ListAttribute{
				MarkdownDescription: "User ids, in turn order. The first is on call from the day the rotation is made. " +
					"Each must be a member of the account; `tissue_mast_person` looks them up by email.",
				ElementType:   types.StringType,
				Optional:      true,
				Computed:      true,
				Validators:    []validator.List{listvalidator.SizeAtMost(50)},
				PlanModifiers: []planmodifier.List{listplanmodifier.UseStateForUnknown()},
			},
			"created_at": computedString("When the rotation was made. Turns are counted from here."),
		},
	}
}

func (r *rotationResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if c, ok := req.ProviderData.(*Client); ok {
		r.client = c
	}
}

func (r *rotationResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg rotationModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() || cfg.Handoff.IsUnknown() || cfg.LengthHours.IsUnknown() {
		return
	}
	custom := cfg.Handoff.ValueString() == "custom"
	switch {
	case custom && (cfg.LengthHours.IsNull() || cfg.LengthHours.ValueInt64() == 0):
		resp.Diagnostics.AddAttributeError(path.Root("length_hours"), "A custom handoff needs length_hours",
			"Set length_hours to the shift length, 1 to 720.")
	case !custom && !cfg.Handoff.IsNull() && !cfg.LengthHours.IsNull() && cfg.LengthHours.ValueInt64() != 0:
		resp.Diagnostics.AddAttributeError(path.Root("length_hours"), "length_hours is for a custom handoff",
			`Set handoff = "custom", or remove length_hours.`)
	}
}

// ModifyPlan follows the server when a rotation leaves custom: it drops the
// shift length to 0 on its own.
func (r *rotationResource) ModifyPlan(ctx context.Context, req resource.ModifyPlanRequest, resp *resource.ModifyPlanResponse) {
	if req.Plan.Raw.IsNull() || req.State.Raw.IsNull() {
		return
	}
	var handoff types.String
	var configLength types.Int64
	resp.Diagnostics.Append(req.Plan.GetAttribute(ctx, path.Root("handoff"), &handoff)...)
	resp.Diagnostics.Append(req.Config.GetAttribute(ctx, path.Root("length_hours"), &configLength)...)
	if resp.Diagnostics.HasError() || !configLength.IsNull() {
		return
	}
	switch {
	case handoff.IsUnknown():
		resp.Plan.SetAttribute(ctx, path.Root("length_hours"), types.Int64Unknown())
	case handoff.ValueString() != "custom":
		resp.Plan.SetAttribute(ctx, path.Root("length_hours"), types.Int64Value(0))
	}
}

func (r *rotationResource) body(ctx context.Context, plan, prior *rotationModel) (map[string]any, error) {
	body := map[string]any{}
	str := func(key string, v, old types.String) {
		if !v.IsNull() && !v.IsUnknown() && (prior == nil || !v.Equal(old)) {
			body[key] = v.ValueString()
		}
	}
	num := func(key string, v, old types.Int64) {
		if !v.IsNull() && !v.IsUnknown() && (prior == nil || !v.Equal(old)) {
			body[key] = v.ValueInt64()
		}
	}
	old := prior
	if old == nil {
		old = &rotationModel{}
	}
	str("name", plan.Name, old.Name)
	str("handoff", plan.Handoff, old.Handoff)
	num("handoff_day", plan.HandoffDay, old.HandoffDay)
	str("handoff_time", plan.HandoffTime, old.HandoffTime)
	str("tz", plan.TZ, old.TZ)
	// Leaving custom zeroes the length server-side; sending the old
	// length along with the new handoff would be refused.
	if plan.Handoff.ValueString() == "custom" || plan.Handoff.IsNull() || plan.Handoff.IsUnknown() {
		num("length_hours", plan.LengthHours, old.LengthHours)
	}
	if !plan.Order.IsNull() && !plan.Order.IsUnknown() && (prior == nil || !plan.Order.Equal(old.Order)) {
		order := []string{}
		if d := plan.Order.ElementsAs(ctx, &order, false); d.HasError() {
			return nil, fmt.Errorf("reading order")
		}
		body["order"] = order
	}
	return body, nil
}

func rotationFromAPI(sc *apiSchedule) *rotationModel {
	order := make([]types.String, 0, len(sc.Order))
	for _, id := range sc.Order {
		order = append(order, types.StringValue(id))
	}
	list, _ := types.ListValueFrom(context.Background(), types.StringType, order)
	return &rotationModel{
		ID:          types.StringValue(sc.ID),
		Name:        types.StringValue(sc.Name),
		Handoff:     types.StringValue(sc.Handoff),
		HandoffDay:  types.Int64Value(sc.HandoffDay),
		HandoffTime: types.StringValue(sc.HandoffTime),
		LengthHours: types.Int64Value(sc.LengthHours),
		TZ:          types.StringValue(sc.TZ),
		Order:       list,
		CreatedAt:   types.StringValue(sc.CreatedAt),
	}
}

func (r *rotationResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan rotationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body, err := r.body(ctx, &plan, nil)
	if err != nil {
		resp.Diagnostics.AddError("Bad order", err.Error())
		return
	}
	sc, err := r.client.CreateSchedule(ctx, body)
	if err != nil {
		resp.Diagnostics.AddError("Creating the rotation failed", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, rotationFromAPI(sc))...)
}

func (r *rotationResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state rotationModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	sc, err := r.client.GetSchedule(ctx, state.ID.ValueString())
	if isNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Reading the rotation failed", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, rotationFromAPI(sc))...)
}

func (r *rotationResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, prior rotationModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &prior)...)
	if resp.Diagnostics.HasError() {
		return
	}
	body, err := r.body(ctx, &plan, &prior)
	if err != nil {
		resp.Diagnostics.AddError("Bad order", err.Error())
		return
	}
	id := prior.ID.ValueString()
	// The server writes the whole row back, overrides included, so an
	// override added to this rotation in the same apply must not land
	// between its read and write.
	unlock := r.client.lockRotation(id)
	defer unlock()
	var sc *apiSchedule
	if len(body) == 0 {
		sc, err = r.client.GetSchedule(ctx, id)
	} else {
		sc, err = r.client.UpdateSchedule(ctx, id, body)
	}
	if err != nil {
		resp.Diagnostics.AddError("Updating the rotation failed", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, rotationFromAPI(sc))...)
}

func (r *rotationResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var id types.String
	resp.Diagnostics.Append(req.State.GetAttribute(ctx, path.Root("id"), &id)...)
	if resp.Diagnostics.HasError() {
		return
	}
	unlock := r.client.lockRotation(id.ValueString())
	defer unlock()
	if err := r.client.DeleteSchedule(ctx, id.ValueString()); err != nil && !isNotFound(err) {
		resp.Diagnostics.AddError("Deleting the rotation failed", err.Error())
	}
}

func (r *rotationResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	sc, err := r.client.FindSchedule(ctx, req.ID)
	if err != nil {
		resp.Diagnostics.AddError("Finding the rotation failed", err.Error())
		return
	}
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), sc.ID)...)
}

var (
	_ resource.ResourceWithImportState    = &rotationResource{}
	_ resource.ResourceWithModifyPlan     = &rotationResource{}
	_ resource.ResourceWithValidateConfig = &rotationResource{}
)
