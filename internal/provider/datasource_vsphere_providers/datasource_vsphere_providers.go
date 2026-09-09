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
	"github.com/vmware/terraform-provider-sspi/internal/provider/pagination"
)

var _ datasource.DataSource = &VsphereProvidersDataSource{}

func NewVsphereProvidersDataSource() datasource.DataSource {
	return &VsphereProvidersDataSource{}
}

type VsphereProvidersDataSource struct {
	client *api_client.ClientWithResponses
}

type VsphereProviderSummaryModel struct {
	ID     types.String `tfsdk:"id"`
	Server types.String `tfsdk:"server"`
	User   types.String `tfsdk:"user"`
}

type VsphereProvidersDataSourceModel struct {
	Results []VsphereProviderSummaryModel `tfsdk:"results"`
}

func (d *VsphereProvidersDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vsphere_providers"
}

func (d *VsphereProvidersDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Lists every vCenter provider registration known to the SSPI appliance.\n\n" +
			"Corresponds to `GET /sspi/providers`, walking every page of results. Use `data.sspi_vsphere_provider` " +
			"for the full details of a specific provider.",
		Attributes: map[string]schema.Attribute{
			"results": schema.ListNestedAttribute{
				MarkdownDescription: "The vCenter providers known to the SSPI appliance.",
				Computed:            true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id":     schema.StringAttribute{Computed: true, MarkdownDescription: "Unique identifier of the provider registration."},
						"server": schema.StringAttribute{Computed: true, MarkdownDescription: "vCenter server IP or FQDN."},
						"user":   schema.StringAttribute{Computed: true, MarkdownDescription: "vCenter username used for this registration."},
					},
				},
			},
		},
	}
}

func (d *VsphereProvidersDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *VsphereProvidersDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data VsphereProvidersDataSourceModel

	tflog.Debug(ctx, "Reading SSPI Vsphere Providers Data Source")

	configs, err := pagination.FetchAll(func(offset int) ([]api_client.VsphereProviderDefinition, int, error) {
		o := offset
		readResp, err := d.client.GetAllVsphereProvidersWithResponse(ctx, &api_client.GetAllVsphereProvidersParams{Offset: &o})
		if err != nil {
			return nil, 0, err
		}
		if readResp.StatusCode() != http.StatusOK || readResp.JSON200 == nil {
			return nil, 0, fmt.Errorf("unexpected response status code: %d", readResp.StatusCode())
		}
		var page []api_client.VsphereProviderDefinition
		if readResp.JSON200.Configs != nil {
			page = *readResp.JSON200.Configs
		}
		total := 0
		if readResp.JSON200.TotalResultCount != nil {
			total = int(*readResp.JSON200.TotalResultCount)
		}
		return page, total, nil
	})
	if err != nil {
		resp.Diagnostics.AddError("Error Reading SSPI Vsphere Providers Data Source", "Could not list vSphere providers: "+err.Error())
		return
	}

	for _, cfg := range configs {
		item := VsphereProviderSummaryModel{
			Server: types.StringValue(cfg.Server),
			User:   types.StringValue(cfg.User),
		}
		if cfg.Id != nil {
			item.ID = types.StringValue(*cfg.Id)
		}
		data.Results = append(data.Results, item)
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
