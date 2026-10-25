package provider

import (
	"context"
	"fmt"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework-validators/datasourcevalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"golang.org/x/text/unicode/norm"
)

type clientHolder struct {
	client *Client
}

func (h *clientHolder) Configure(_ context.Context, req datasource.ConfigureRequest, _ *datasource.ConfigureResponse) {
	if c, ok := req.ProviderData.(*Client); ok {
		h.client = c
	}
}

type personDataSource struct{ clientHolder }

func newPersonDataSource() datasource.DataSource { return &personDataSource{} }

type personModel struct {
	Email  types.String `tfsdk:"email"`
	UserID types.String `tfsdk:"user_id"`
	Role   types.String `tfsdk:"role"`
	Phones types.Int64  `tfsdk:"phones"`
}

func (d *personDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_mast_person"
}

func (d *personDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A member of the account, looked up by email. Rotations, overrides and policy steps " +
			"take the `user_id`.",
		Attributes: map[string]schema.Attribute{
			"email":   schema.StringAttribute{Required: true, MarkdownDescription: "The member's email. Case doesn't matter."},
			"user_id": schema.StringAttribute{Computed: true, MarkdownDescription: "The member's user id."},
			"role":    schema.StringAttribute{Computed: true, MarkdownDescription: "`owner`, `admin` or `developer`."},
			"phones":  schema.Int64Attribute{Computed: true, MarkdownDescription: "How many phones Mast pages for them."},
		},
	}
}

func (d *personDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg personModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	people, err := d.client.People(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Listing the account's people failed", err.Error())
		return
	}
	want := strings.TrimSpace(cfg.Email.ValueString())
	for _, p := range people {
		if strings.EqualFold(p.Email, want) {
			resp.Diagnostics.Append(resp.State.Set(ctx, &personModel{
				Email:  cfg.Email,
				UserID: types.StringValue(p.UserID),
				Role:   types.StringValue(p.Role),
				Phones: types.Int64Value(p.Phones),
			})...)
			return
		}
	}
	resp.Diagnostics.AddAttributeError(path.Root("email"), "No such member",
		fmt.Sprintf("Nobody with the email %s is a member of this account. Invite them first.", want))
}

type peopleDataSource struct{ clientHolder }

func newPeopleDataSource() datasource.DataSource { return &peopleDataSource{} }

type peopleModel struct {
	People []personModel `tfsdk:"people"`
}

func (d *peopleDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_mast_people"
}

func (d *peopleDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Every member of the account.",
		Attributes: map[string]schema.Attribute{
			"people": schema.ListNestedAttribute{
				Computed: true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"email":   schema.StringAttribute{Computed: true},
						"user_id": schema.StringAttribute{Computed: true},
						"role":    schema.StringAttribute{Computed: true},
						"phones":  schema.Int64Attribute{Computed: true},
					},
				},
			},
		},
	}
}

func (d *peopleDataSource) Read(ctx context.Context, _ datasource.ReadRequest, resp *datasource.ReadResponse) {
	people, err := d.client.People(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Listing the account's people failed", err.Error())
		return
	}
	out := peopleModel{People: []personModel{}}
	for _, p := range people {
		out.People = append(out.People, personModel{
			Email:  types.StringValue(p.Email),
			UserID: types.StringValue(p.UserID),
			Role:   types.StringValue(p.Role),
			Phones: types.Int64Value(p.Phones),
		})
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &out)...)
}

type rotationDataSource struct{ clientHolder }

func newRotationDataSource() datasource.DataSource { return &rotationDataSource{} }

type rotationDataModel struct {
	ID           types.String `tfsdk:"id"`
	Name         types.String `tfsdk:"name"`
	Handoff      types.String `tfsdk:"handoff"`
	HandoffDay   types.Int64  `tfsdk:"handoff_day"`
	HandoffTime  types.String `tfsdk:"handoff_time"`
	LengthHours  types.Int64  `tfsdk:"length_hours"`
	TZ           types.String `tfsdk:"tz"`
	Order        types.List   `tfsdk:"order"`
	OnCallUserID types.String `tfsdk:"on_call_user_id"`
	OnCallUntil  types.String `tfsdk:"on_call_until"`
}

func (d *rotationDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_mast_rotation"
}

func (d *rotationDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "An on-call rotation, by id or by name. Useful for pointing a policy at a rotation " +
			"someone made in the app.",
		Attributes: map[string]schema.Attribute{
			"id":              schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "The rotation id."},
			"name":            schema.StringAttribute{Optional: true, Computed: true, MarkdownDescription: "The rotation name. It has to be unique to look it up this way."},
			"handoff":         schema.StringAttribute{Computed: true},
			"handoff_day":     schema.Int64Attribute{Computed: true},
			"handoff_time":    schema.StringAttribute{Computed: true},
			"length_hours":    schema.Int64Attribute{Computed: true},
			"tz":              schema.StringAttribute{Computed: true},
			"order":           schema.ListAttribute{Computed: true, ElementType: types.StringType},
			"on_call_user_id": schema.StringAttribute{Computed: true, MarkdownDescription: "Who is on call as of the read."},
			"on_call_until":   schema.StringAttribute{Computed: true, MarkdownDescription: "When that shift ends."},
		},
	}
}

func (d *rotationDataSource) ConfigValidators(context.Context) []datasource.ConfigValidator {
	return []datasource.ConfigValidator{
		datasourcevalidator.ExactlyOneOf(path.MatchRoot("id"), path.MatchRoot("name")),
	}
}

func (d *rotationDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg rotationDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	var sc *apiSchedule
	var err error
	if !cfg.ID.IsNull() {
		sc, err = d.client.GetSchedule(ctx, cfg.ID.ValueString())
	} else {
		sc, err = d.client.FindSchedule(ctx, cfg.Name.ValueString())
	}
	if err != nil {
		resp.Diagnostics.AddError("Finding the rotation failed", err.Error())
		return
	}
	r := rotationFromAPI(sc)
	out := rotationDataModel{
		ID: r.ID, Name: r.Name, Handoff: r.Handoff, HandoffDay: r.HandoffDay, HandoffTime: r.HandoffTime,
		LengthHours: r.LengthHours, TZ: r.TZ, Order: r.Order,
		OnCallUserID: types.StringNull(), OnCallUntil: types.StringNull(),
	}
	if sc.Now != nil {
		out.OnCallUserID = types.StringValue(sc.Now.UserID)
		out.OnCallUntil = types.StringValue(sc.Now.Until)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &out)...)
}

type channelDataSource struct{ clientHolder }

func newChannelDataSource() datasource.DataSource { return &channelDataSource{} }

type channelDataModel struct {
	Name     types.String `tfsdk:"name"`
	ID       types.String `tfsdk:"id"`
	Kind     types.String `tfsdk:"kind"`
	URL      types.String `tfsdk:"url"`
	PolicyID types.String `tfsdk:"policy_id"`
}

func (d *channelDataSource) Metadata(_ context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_mast_channel"
}

func (d *channelDataSource) Schema(_ context.Context, _ datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "A channel or vital by name, for wiring one made in the app into other configuration.",
		Attributes: map[string]schema.Attribute{
			"name":      schema.StringAttribute{Required: true, MarkdownDescription: "The channel's name."},
			"id":        schema.StringAttribute{Computed: true, MarkdownDescription: "The channel id."},
			"kind":      schema.StringAttribute{Computed: true, MarkdownDescription: "`channel` or `vital`."},
			"url":       schema.StringAttribute{Computed: true, Sensitive: true, MarkdownDescription: "The ingest URL, key included."},
			"policy_id": schema.StringAttribute{Computed: true, MarkdownDescription: "The policy it follows, or empty."},
		},
	}
}

func (d *channelDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var cfg channelDataModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	list, err := d.client.Channels(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Listing channels failed", err.Error())
		return
	}
	want := norm.NFC.String(strings.TrimSpace(cfg.Name.ValueString()))
	for _, ch := range list {
		if ch.Name != want {
			continue
		}
		policy, err := d.client.PolicyOf(ctx, ch.ID)
		if err != nil {
			resp.Diagnostics.AddError("Listing policies failed", err.Error())
			return
		}
		resp.Diagnostics.Append(resp.State.Set(ctx, &channelDataModel{
			Name:     cfg.Name,
			ID:       types.StringValue(ch.ID),
			Kind:     types.StringValue(ch.Kind),
			URL:      types.StringPointerValue(ch.URL),
			PolicyID: types.StringValue(policy),
		})...)
		return
	}
	resp.Diagnostics.AddAttributeError(path.Root("name"), "No such channel",
		fmt.Sprintf("There is no live channel or vital named %q.", want))
}

var (
	_ datasource.DataSourceWithConfigure        = &personDataSource{}
	_ datasource.DataSourceWithConfigure        = &peopleDataSource{}
	_ datasource.DataSourceWithConfigValidators = &rotationDataSource{}
	_ datasource.DataSourceWithConfigure        = &channelDataSource{}
)
