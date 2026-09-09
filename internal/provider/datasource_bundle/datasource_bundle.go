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

	"github.com/vmware/terraform-provider-sspi/internal/client/depot_client"
)

// Ensure provider defined types fully satisfy framework interfaces.
var _ datasource.DataSource = &BundleDataSource{}

func NewBundleDataSource() datasource.DataSource {
	return &BundleDataSource{}
}

type BundleDataSource struct {
	client *depot_client.ClientWithResponses
}

type BundleDataSourceModel struct {
	ID      types.String `tfsdk:"id"`
	Status  types.String `tfsdk:"status"`
	Version types.String `tfsdk:"version"`
}

func (d *BundleDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_bundle"
}

func (d *BundleDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Fetches status and details of a software bundle in SSPI Depot.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier for the software bundle.",
				Required:            true,
			},
			"status": schema.StringAttribute{
				MarkdownDescription: "The current status of the software bundle.",
				Computed:            true,
			},
			"version": schema.StringAttribute{
				MarkdownDescription: "The version of the bundle.",
				Computed:            true,
			},
		},
	}
}

func (d *BundleDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	clientProvider, ok := req.ProviderData.(interface {
		GetDepot() *depot_client.ClientWithResponses
	})

	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Data Source Configure Type",
			fmt.Sprintf("Expected provider data with GetDepot(), got: %T.", req.ProviderData),
		)
		return
	}

	d.client = clientProvider.GetDepot()
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

func (d *BundleDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data BundleDataSourceModel

	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, "Reading SSPI Bundle Data Source", map[string]interface{}{"id": data.ID.ValueString()})

	readResp, err := d.client.GetBundleWithResponse(ctx, data.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(
			"Error Reading SSPI Bundle Data Source",
			"Could not read bundle status: "+err.Error(),
		)
		return
	}

	if readResp.StatusCode() != http.StatusOK || readResp.JSON200 == nil {
		resp.Diagnostics.AddError(
			"Error Reading SSPI Bundle Data Source",
			fmt.Sprintf("Unexpected response status code: %d", readResp.StatusCode()),
		)
		return
	}

	bundle := readResp.JSON200
	if bundle.Status != nil {
		data.Status = types.StringValue(string(*bundle.Status))
	}
	if bundle.BundleVersion != nil {
		data.Version = types.StringValue(*bundle.BundleVersion)
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
