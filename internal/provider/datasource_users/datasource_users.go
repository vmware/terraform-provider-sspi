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

var _ datasource.DataSource = &UsersDataSource{}

func NewUsersDataSource() datasource.DataSource {
	return &UsersDataSource{}
}

type UsersDataSource struct {
	client *iam_client.ClientWithResponses
}

type UserSummaryModel struct {
	Name        types.String `tfsdk:"name"`
	DisplayName types.String `tfsdk:"display_name"`
	UserType    types.String `tfsdk:"user_type"`
}

type UsersDataSourceModel struct {
	Results []UserSummaryModel `tfsdk:"results"`
}

func (d *UsersDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_users"
}

func (d *UsersDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Lists every local and remote (LDAP) user or group known to the SSPI appliance's IAM " +
			"subsystem.\n\nCorresponds to `GET /sspi/iam/users`, walking every page of results.",
		Attributes: map[string]schema.Attribute{
			"results": schema.ListNestedAttribute{
				MarkdownDescription: "The users and groups known to the SSPI appliance.",
				Computed:            true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"name":         schema.StringAttribute{Computed: true, MarkdownDescription: "The user or group's name."},
						"display_name": schema.StringAttribute{Computed: true, MarkdownDescription: "Human-readable display name."},
						"user_type":    schema.StringAttribute{Computed: true, MarkdownDescription: "`LOCAL_USER`, `REMOTE_USER`, or `REMOTE_GROUP`."},
					},
				},
			},
		},
	}
}

func (d *UsersDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *UsersDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data UsersDataSourceModel

	tflog.Debug(ctx, "Reading SSPI Users Data Source")

	users, err := pagination.FetchAll(func(offset int) ([]iam_client.UserInfo, int, error) {
		o := offset
		readResp, err := d.client.GetAllUsersInfoWithResponse(ctx, &iam_client.GetAllUsersInfoParams{Offset: &o})
		if err != nil {
			return nil, 0, err
		}
		if readResp.StatusCode() != http.StatusOK || readResp.JSON200 == nil {
			return nil, 0, fmt.Errorf("unexpected response status code: %d", readResp.StatusCode())
		}
		var page []iam_client.UserInfo
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
		resp.Diagnostics.AddError("Error Reading SSPI Users Data Source", "Could not list users: "+err.Error())
		return
	}

	for _, u := range users {
		item := UserSummaryModel{}
		if u.Name != nil {
			item.Name = types.StringValue(*u.Name)
		}
		if u.DisplayName != nil {
			item.DisplayName = types.StringValue(*u.DisplayName)
		}
		if u.UserType != nil {
			item.UserType = types.StringValue(string(*u.UserType))
		}
		data.Results = append(data.Results, item)
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
