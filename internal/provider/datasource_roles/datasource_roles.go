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

	"github.com/vmware/terraform-provider-sspi/internal/client/iam_client"
	"github.com/vmware/terraform-provider-sspi/internal/provider/pagination"
)

var _ datasource.DataSource = &RolesDataSource{}

func NewRolesDataSource() datasource.DataSource {
	return &RolesDataSource{}
}

type RolesDataSource struct {
	client *iam_client.ClientWithResponses
}

type RoleSummaryModel struct {
	Role        types.String `tfsdk:"role"`
	DisplayName types.String `tfsdk:"display_name"`
}

type RolesDataSourceModel struct {
	Results []RoleSummaryModel `tfsdk:"results"`
}

func (d *RolesDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_roles"
}

func (d *RolesDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Lists every role available to be assigned on the SSPI appliance.\n\n" +
			"Corresponds to `GET /sspi/iam/roles`, walking every page of results.",
		Attributes: map[string]schema.Attribute{
			"results": schema.ListNestedAttribute{
				MarkdownDescription: "The roles available to be assigned via `sspi_role_binding`.",
				Computed:            true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"role":         schema.StringAttribute{Computed: true, MarkdownDescription: "Machine-readable role name (e.g. `ENTERPRISE_ADMIN`, `AUDITOR`)."},
						"display_name": schema.StringAttribute{Computed: true, MarkdownDescription: "Human-readable role name."},
					},
				},
			},
		},
	}
}

func (d *RolesDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	clientProvider, ok := req.ProviderData.(interface {
		GetIAM() *iam_client.ClientWithResponses
	})

	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Data Source Configure Type",
			fmt.Sprintf("Expected provider data with GetIAM(), got: %T.", req.ProviderData),
		)
		return
	}

	d.client = clientProvider.GetIAM()
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

func (d *RolesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data RolesDataSourceModel

	tflog.Debug(ctx, "Reading SSPI Roles Data Source")

	roles, err := pagination.FetchAll(func(offset int) ([]iam_client.Role, int, error) {
		o := offset
		readResp, err := d.client.GetAllRolesInfoWithResponse(ctx, &iam_client.GetAllRolesInfoParams{Offset: &o})
		if err != nil {
			return nil, 0, err
		}
		if readResp.StatusCode() != http.StatusOK || readResp.JSON200 == nil {
			return nil, 0, fmt.Errorf("unexpected response status code: %d", readResp.StatusCode())
		}
		var page []iam_client.Role
		if readResp.JSON200.Results != nil {
			page = *readResp.JSON200.Results
		}
		total := 0
		if readResp.JSON200.TotalResultCount != nil {
			total = int(*readResp.JSON200.TotalResultCount)
		}
		return page, total, nil
	})
	if err != nil {
		resp.Diagnostics.AddError("Error Reading SSPI Roles Data Source", "Could not list roles: "+err.Error())
		return
	}

	for _, ro := range roles {
		item := RoleSummaryModel{
			Role: types.StringValue(string(ro.Role)),
		}
		if ro.DisplayName != nil {
			item.DisplayName = types.StringValue(*ro.DisplayName)
		}
		data.Results = append(data.Results, item)
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
