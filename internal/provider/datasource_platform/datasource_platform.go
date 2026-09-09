// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.

package provider

import (
	"context"
	"fmt"
	"net/http"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/vmware/terraform-provider-sspi/internal/client/api_client"
)

// Ensure provider defined types fully satisfy framework interfaces.
var _ datasource.DataSource = &PlatformDataSource{}

func NewPlatformDataSource() datasource.DataSource {
	return &PlatformDataSource{}
}

type PlatformDataSource struct {
	client *api_client.ClientWithResponses
}

type PlatformDataSourceModel struct {
	ID          types.String `tfsdk:"id"`
	ClusterName types.String `tfsdk:"cluster_name"`
	Datastore   types.String `tfsdk:"datastore"`
	Domain      types.String `tfsdk:"domain"`
	Status      types.String `tfsdk:"status"`
}

func (d *PlatformDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_platform"
}

func (d *PlatformDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Fetches details of an SSP Platform instance in SSPI.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier for the platform.",
				Required:            true,
			},
			"cluster_name": schema.StringAttribute{
				MarkdownDescription: "The name of the vSphere cluster.",
				Computed:            true,
			},
			"datastore": schema.StringAttribute{
				MarkdownDescription: "The name of the vSphere datastore.",
				Computed:            true,
			},
			"domain": schema.StringAttribute{
				MarkdownDescription: "The domain name for the platform instance.",
				Computed:            true,
			},
			"status": schema.StringAttribute{
				MarkdownDescription: "The status of the platform deployment.",
				Computed:            true,
			},
		},
	}
}

func (d *PlatformDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *PlatformDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data PlatformDataSourceModel

	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, "Reading SSPI Platform Data Source", map[string]interface{}{"id": data.ID.ValueString()})

	readResp, err := d.client.GetPlatformConfigWithResponse(ctx, data.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(
			"Error Reading SSPI Platform Data Source",
			"Could not read platform config: "+err.Error(),
		)
		return
	}

	if readResp.StatusCode() != http.StatusOK || readResp.JSON200 == nil {
		resp.Diagnostics.AddError(
			"Error Reading SSPI Platform Data Source",
			fmt.Sprintf("Unexpected response status code: %d", readResp.StatusCode()),
		)
		return
	}

	cfg := readResp.JSON200
	if cfg.Compute.ClusterName != nil {
		data.ClusterName = types.StringValue(*cfg.Compute.ClusterName)
	}
	if cfg.Compute.ContentDatastoreName != nil {
		data.Datastore = types.StringValue(*cfg.Compute.ContentDatastoreName)
	}
	if cfg.Network.SearchDomain != "" {
		data.Domain = types.StringValue(cfg.Network.SearchDomain)
	}

	statusResp, err := d.client.GetPlatformLcmStatusWithResponse(ctx, data.ID.ValueString())
	if err == nil && statusResp.JSON200 != nil && statusResp.JSON200.Phase != nil {
		data.Status = types.StringValue(string(*statusResp.JSON200.Phase))
	} else {
		data.Status = types.StringValue("UNKNOWN")
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
