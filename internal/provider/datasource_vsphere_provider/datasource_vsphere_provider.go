// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.

package provider

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/vmware/terraform-provider-sspi/internal/client/api_client"
)

// Ensure provider defined types fully satisfy framework interfaces.
var _ datasource.DataSource = &VsphereProviderDataSource{}

func NewVsphereProviderDataSource() datasource.DataSource {
	return &VsphereProviderDataSource{}
}

type VsphereProviderDataSource struct {
	client *api_client.ClientWithResponses
}

type VsphereProviderDataSourceModel struct {
	ID          types.String `tfsdk:"id"`
	Server      types.String `tfsdk:"server"`
	User        types.String `tfsdk:"user"`
	Certificate types.String `tfsdk:"certificate"`
	CreatedAt   types.String `tfsdk:"created_at"`
	UpdatedAt   types.String `tfsdk:"updated_at"`
}

func (d *VsphereProviderDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vsphere_provider"
}

func (d *VsphereProviderDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Fetches details of a registered vSphere Provider in SSPI.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier for the vSphere provider.",
				Required:            true,
			},
			"server": schema.StringAttribute{
				MarkdownDescription: "The IP address or FQDN of the vCenter Server.",
				Computed:            true,
			},
			"user": schema.StringAttribute{
				MarkdownDescription: "The username used to connect to vCenter.",
				Computed:            true,
			},
			"certificate": schema.StringAttribute{
				MarkdownDescription: "The certificate of the vCenter Server.",
				Computed:            true,
			},
			"created_at": schema.StringAttribute{
				MarkdownDescription: "Resource creation time.",
				Computed:            true,
			},
			"updated_at": schema.StringAttribute{
				MarkdownDescription: "Resource last update time.",
				Computed:            true,
			},
		},
	}
}

func (d *VsphereProviderDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	clientProvider, ok := req.ProviderData.(interface {
		GetAPI() *api_client.ClientWithResponses
	})

	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Data Source Configure Type",
			fmt.Sprintf("Expected provider data with GetAPI(), got: %T.", req.ProviderData),
		)
		return
	}

	d.client = clientProvider.GetAPI()
	if d.client == nil {
		resp.Diagnostics.AddError(
			"SSPI Appliance Not Configured",
			"This data source requires the SSPI appliance connection to be configured on the provider "+
				"(\"sspi_host\", \"sspi_username\", and \"sspi_password\", or the SSPI_HOST, SSPI_USERNAME, "+
				"and SSPI_PASSWORD environment variables).",
		)
		return
	}
}

func (d *VsphereProviderDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data VsphereProviderDataSourceModel

	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, "Reading SSPI vSphere Provider Data Source", map[string]interface{}{"id": data.ID.ValueString()})

	readResp, err := d.client.GetVsphereProviderWithResponse(ctx, data.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(
			"Error Reading SSPI vSphere Provider Data Source",
			"Could not read vSphere provider: "+err.Error(),
		)
		return
	}

	if readResp.StatusCode() != http.StatusOK || readResp.JSON200 == nil {
		resp.Diagnostics.AddError(
			"Error Reading SSPI vSphere Provider Data Source",
			fmt.Sprintf("Unexpected response status code: %d", readResp.StatusCode()),
		)
		return
	}

	prov := readResp.JSON200
	if prov.Id != nil {
		data.ID = types.StringValue(*prov.Id)
	}
	data.Server = types.StringValue(prov.Server)
	data.User = types.StringValue(prov.User)
	if prov.Certificate != nil {
		data.Certificate = types.StringValue(*prov.Certificate)
	}
	if prov.UnderscoreCreateTime != nil {
		data.CreatedAt = types.StringValue(time.UnixMilli(*prov.UnderscoreCreateTime).UTC().Format(time.RFC3339))
	}
	if prov.UnderscoreLastModifiedTime != nil {
		data.UpdatedAt = types.StringValue(time.UnixMilli(*prov.UnderscoreLastModifiedTime).UTC().Format(time.RFC3339))
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
