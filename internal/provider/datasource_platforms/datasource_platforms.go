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

var _ datasource.DataSource = &PlatformsDataSource{}

func NewPlatformsDataSource() datasource.DataSource {
	return &PlatformsDataSource{}
}

type PlatformsDataSource struct {
	client *api_client.ClientWithResponses
}

type PlatformSummaryModel struct {
	ID           types.String `tfsdk:"id"`
	InstanceName types.String `tfsdk:"instance_name"`
	SspType      types.String `tfsdk:"ssp_type"`
	FormFactor   types.String `tfsdk:"form_factor"`
	DesiredState types.String `tfsdk:"desired_state"`
}

type PlatformsDataSourceModel struct {
	Results []PlatformSummaryModel `tfsdk:"results"`
}

func (d *PlatformsDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_platforms"
}

func (d *PlatformsDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Lists every SSP/Avi Operations platform deployment known to the SSPI appliance.\n\n" +
			"Corresponds to `GET /sspi/platforms`, walking every page of results. Use `data.sspi_platform` " +
			"for the full configuration of a specific platform.",
		Attributes: map[string]schema.Attribute{
			"results": schema.ListNestedAttribute{
				MarkdownDescription: "The platforms known to the SSPI appliance.",
				Computed:            true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id":            schema.StringAttribute{Computed: true, MarkdownDescription: "Unique identifier of the platform."},
						"instance_name": schema.StringAttribute{Computed: true, MarkdownDescription: "Cluster/instance name of the platform."},
						"ssp_type":      schema.StringAttribute{Computed: true, MarkdownDescription: "Platform instance type (e.g. `ATP`, `AVI_OPERATIONS`)."},
						"form_factor":   schema.StringAttribute{Computed: true, MarkdownDescription: "Deployment form factor."},
						"desired_state": schema.StringAttribute{Computed: true, MarkdownDescription: "Most recently requested desired state for the platform's LCM workflow."},
					},
				},
			},
		},
	}
}

func (d *PlatformsDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *PlatformsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data PlatformsDataSourceModel

	tflog.Debug(ctx, "Reading SSPI Platforms Data Source")

	configs, err := pagination.FetchAll(func(offset int) ([]api_client.PlatformFullConfig, int, error) {
		o := offset
		readResp, err := d.client.GetAllPlatformsWithResponse(ctx, &api_client.GetAllPlatformsParams{Offset: &o})
		if err != nil {
			return nil, 0, err
		}
		if readResp.StatusCode() != http.StatusOK || readResp.JSON200 == nil {
			return nil, 0, fmt.Errorf("unexpected response status code: %d", readResp.StatusCode())
		}
		var page []api_client.PlatformFullConfig
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
		resp.Diagnostics.AddError("Error Reading SSPI Platforms Data Source", "Could not list platforms: "+err.Error())
		return
	}

	for _, cfg := range configs {
		item := PlatformSummaryModel{}
		if cfg.Id != nil {
			item.ID = types.StringValue(*cfg.Id)
		}
		item.InstanceName = types.StringValue(cfg.Service.InstanceName)
		if cfg.System.SspType != nil {
			item.SspType = types.StringValue(string(*cfg.System.SspType))
		}
		if cfg.System.FormFactor != nil {
			item.FormFactor = types.StringValue(string(*cfg.System.FormFactor))
		}
		if cfg.DesiredState != nil {
			item.DesiredState = types.StringValue(string(*cfg.DesiredState))
		}
		data.Results = append(data.Results, item)
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
