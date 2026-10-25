package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

// Overrides have no routes of their own. Each change reads the rotation,
// edits its list and sends the whole list back, ids included, which is how
// the server tells the ones to keep from new ones.
type overrideResource struct {
	client *Client
}

func newOverrideResource() resource.Resource { return &overrideResource{} }

type overrideModel struct {
	ID           types.String `tfsdk:"id"`
	OverrideID   types.String `tfsdk:"override_id"`
	RotationID   types.String `tfsdk:"rotation_id"`
	UserID       types.String `tfsdk:"user_id"`
	CoversUserID types.String `tfsdk:"covers_user_id"`
	Start        types.String `tfsdk:"start"`
	End          types.String `tfsdk:"end"`
}

func (r *overrideResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_mast_override"
}

func (r *overrideResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A stretch of time when someone else holds a rotation: a swap, or cover for a holiday. " +
			"Times are local to the rotation's `tz`.",
		Attributes: map[string]schema.Attribute{
			"id":          computedString("`<rotation_id>/<override_id>`."),
			"override_id": computedString("The override's own id, `ov_...`."),
			"rotation_id": schema.StringAttribute{
				MarkdownDescription: "The rotation this override belongs to.",
				Required:            true,
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"user_id": schema.StringAttribute{
				MarkdownDescription: "Who is on call instead.",
				Required:            true,
				Validators:          []validator.String{stringvalidator.LengthBetween(1, 64)},
			},
			"covers_user_id": schema.StringAttribute{
				MarkdownDescription: "Only stand in for this person. Without it the override takes every shift it overlaps.",
				Optional:            true,
				Validators:          []validator.String{stringvalidator.LengthBetween(1, 64)},
			},
			"start": schema.StringAttribute{
				MarkdownDescription: "A date, `YYYY-MM-DD`, meaning 00:00 that day, or a time, `YYYY-MM-DDTHH:MM`.",
				Required:            true,
				Validators:          []validator.String{overrideBoundValidator{}},
			},
			"end": schema.StringAttribute{
				MarkdownDescription: "A date, meaning the end of that day, or a time, where the override hands back. " +
					"Two dates cover whole days, both included.",
				Required:   true,
				Validators: []validator.String{overrideBoundValidator{}},
			},
		},
	}
}

func (r *overrideResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if c, ok := req.ProviderData.(*Client); ok {
		r.client = c
	}
}

func (r *overrideResource) ValidateConfig(ctx context.Context, req resource.ValidateConfigRequest, resp *resource.ValidateConfigResponse) {
	var cfg overrideModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() || cfg.Start.IsUnknown() || cfg.End.IsUnknown() || cfg.Start.IsNull() || cfg.End.IsNull() {
		return
	}
	start, ok1 := overrideStart(cfg.Start.ValueString())
	end, ok2 := overrideEnd(cfg.End.ValueString())
	if ok1 && ok2 && !end.After(start) {
		resp.Diagnostics.AddAttributeError(path.Root("end"), "The override ends before it starts",
			fmt.Sprintf("%s to %s is no time at all.", cfg.Start.ValueString(), cfg.End.ValueString()))
	}
}

func (m *overrideModel) api() apiOverride {
	return apiOverride{
		ID:           m.OverrideID.ValueString(),
		UserID:       m.UserID.ValueString(),
		CoversUserID: m.CoversUserID.ValueString(),
		Start:        m.Start.ValueString(),
		End:          m.End.ValueString(),
	}
}

func overrideFromAPI(rotationID string, o apiOverride) *overrideModel {
	m := &overrideModel{
		ID:           types.StringValue(rotationID + "/" + o.ID),
		OverrideID:   types.StringValue(o.ID),
		RotationID:   types.StringValue(rotationID),
		UserID:       types.StringValue(o.UserID),
		CoversUserID: types.StringNull(),
		Start:        types.StringValue(o.Start),
		End:          types.StringValue(o.End),
	}
	if o.CoversUserID != "" {
		m.CoversUserID = types.StringValue(o.CoversUserID)
	}
	return m
}

// edit runs change over a rotation's overrides under the rotation's lock and
// saves the result.
func (r *overrideResource) edit(ctx context.Context, rotationID string, change func([]apiOverride) ([]apiOverride, error)) (*apiSchedule, error) {
	unlock := r.client.lockRotation(rotationID)
	defer unlock()
	sc, err := r.client.GetSchedule(ctx, rotationID)
	if err != nil {
		return nil, err
	}
	list, err := change(sc.Overrides)
	if err != nil {
		return nil, err
	}
	return r.client.UpdateSchedule(ctx, rotationID, map[string]any{"overrides": list})
}

func (r *overrideResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var plan overrideModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	if resp.Diagnostics.HasError() {
		return
	}
	rotationID := plan.RotationID.ValueString()
	before := map[string]bool{}
	sc, err := r.edit(ctx, rotationID, func(list []apiOverride) ([]apiOverride, error) {
		for _, o := range list {
			before[o.ID] = true
		}
		o := plan.api()
		o.ID = ""
		return append(list, o), nil
	})
	if err != nil {
		resp.Diagnostics.AddError("Adding the override failed", err.Error())
		return
	}
	for _, o := range sc.Overrides {
		if !before[o.ID] {
			resp.Diagnostics.Append(resp.State.Set(ctx, overrideFromAPI(rotationID, o))...)
			return
		}
	}
	resp.Diagnostics.AddError("The override vanished", "The rotation was saved but has no new override in it.")
}

func (r *overrideResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var state overrideModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	rotationID := state.RotationID.ValueString()
	sc, err := r.client.GetSchedule(ctx, rotationID)
	if isNotFound(err) {
		resp.State.RemoveResource(ctx)
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Reading the rotation failed", err.Error())
		return
	}
	for _, o := range sc.Overrides {
		if o.ID == state.OverrideID.ValueString() {
			resp.Diagnostics.Append(resp.State.Set(ctx, overrideFromAPI(rotationID, o))...)
			return
		}
	}
	resp.State.RemoveResource(ctx)
}

func (r *overrideResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state overrideModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	rotationID := state.RotationID.ValueString()
	id := state.OverrideID.ValueString()
	sc, err := r.edit(ctx, rotationID, func(list []apiOverride) ([]apiOverride, error) {
		for i := range list {
			if list[i].ID == id {
				o := plan.api()
				o.ID = id
				list[i] = o
				return list, nil
			}
		}
		return nil, fmt.Errorf("override %s is no longer on rotation %s", id, rotationID)
	})
	if err != nil {
		resp.Diagnostics.AddError("Updating the override failed", err.Error())
		return
	}
	for _, o := range sc.Overrides {
		if o.ID == id {
			resp.Diagnostics.Append(resp.State.Set(ctx, overrideFromAPI(rotationID, o))...)
			return
		}
	}
	resp.Diagnostics.AddError("The override vanished", id+" was not on the rotation after the update.")
}

func (r *overrideResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var state overrideModel
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}
	id := state.OverrideID.ValueString()
	_, err := r.edit(ctx, state.RotationID.ValueString(), func(list []apiOverride) ([]apiOverride, error) {
		kept := make([]apiOverride, 0, len(list))
		for _, o := range list {
			if o.ID != id {
				kept = append(kept, o)
			}
		}
		return kept, nil
	})
	if err != nil && !isNotFound(err) {
		resp.Diagnostics.AddError("Removing the override failed", err.Error())
	}
}

// ImportState takes `<rotation>/<override_id>`, the rotation by id or name.
func (r *overrideResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	i := strings.LastIndex(req.ID, "/")
	if i <= 0 || i == len(req.ID)-1 {
		resp.Diagnostics.AddError("Bad import id", "Use <rotation id or name>/<override id>, such as sc_123/ov_456.")
		return
	}
	sc, err := r.client.FindSchedule(ctx, req.ID[:i])
	if err != nil {
		resp.Diagnostics.AddError("Finding the rotation failed", err.Error())
		return
	}
	id := req.ID[i+1:]
	for _, o := range sc.Overrides {
		if o.ID == id {
			resp.Diagnostics.Append(resp.State.Set(ctx, overrideFromAPI(sc.ID, o))...)
			return
		}
	}
	resp.Diagnostics.AddError("No such override", fmt.Sprintf("Rotation %s has no override %s.", sc.ID, id))
}

var (
	_ resource.ResourceWithImportState    = &overrideResource{}
	_ resource.ResourceWithValidateConfig = &overrideResource{}
)
