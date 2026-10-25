package provider

import (
	"context"
	"os"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

const defaultServer = "https://api.tissue.systems"

type tissueProvider struct {
	version string
}

type providerModel struct {
	Token  types.String `tfsdk:"token"`
	Server types.String `tfsdk:"server"`
}

func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &tissueProvider{version: version}
	}
}

func (p *tissueProvider) Metadata(_ context.Context, _ provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "tissue"
	resp.Version = p.version
}

func (p *tissueProvider) Schema(_ context.Context, _ provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages Tissue Mast channels, vitals, rotations and escalation policies.",
		Attributes: map[string]schema.Attribute{
			"token": schema.StringAttribute{
				MarkdownDescription: "An API token (`tok_...`) with the `mast:read` and `mast:write` scopes. " +
					"Falls back to `TISSUE_TOKEN`.",
				Optional:  true,
				Sensitive: true,
			},
			"server": schema.StringAttribute{
				MarkdownDescription: "The API server. Falls back to `TISSUE_SERVER`, then `" + defaultServer + "`.",
				Optional:            true,
			},
		},
	}
}

func (p *tissueProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var cfg providerModel
	resp.Diagnostics.Append(req.Config.Get(ctx, &cfg)...)
	if resp.Diagnostics.HasError() {
		return
	}
	if cfg.Token.IsUnknown() || cfg.Server.IsUnknown() {
		// Known on apply; a plan of a provider configured from another
		// resource's output gets an unconfigured client and no calls.
		return
	}

	token := os.Getenv("TISSUE_TOKEN")
	if !cfg.Token.IsNull() {
		token = cfg.Token.ValueString()
	}
	server := os.Getenv("TISSUE_SERVER")
	if !cfg.Server.IsNull() {
		server = cfg.Server.ValueString()
	}
	if server == "" {
		server = defaultServer
	}
	if token == "" {
		resp.Diagnostics.AddAttributeError(path.Root("token"), "No API token",
			"Set token in the provider block or TISSUE_TOKEN in the environment. "+
				"Make one on the dashboard's API tokens page with the mast:read and mast:write scopes.")
		return
	}

	client := NewClient(server, token, "terraform-provider-tissue/"+p.version)
	resp.ResourceData = client
	resp.DataSourceData = client
}

func (p *tissueProvider) Resources(context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		newChannelResource,
		newVitalResource,
		newRotationResource,
		newOverrideResource,
		newPolicyResource,
	}
}

func (p *tissueProvider) DataSources(context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		newPersonDataSource,
		newPeopleDataSource,
		newRotationDataSource,
		newChannelDataSource,
	}
}
