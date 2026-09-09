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

var _ datasource.DataSource = &RoleBindingsDataSource{}

func NewRoleBindingsDataSource() datasource.DataSource {
	return &RoleBindingsDataSource{}
}

type RoleBindingsDataSource struct {
	client *iam_client.ClientWithResponses
}

type RoleBindingSummaryModel struct {
	ID         types.String   `tfsdk:"id"`
	EntityName types.String   `tfsdk:"entity_name"`
	UserType   types.String   `tfsdk:"user_type"`
	Roles      []types.String `tfsdk:"roles"`
}

type RoleBindingsDataSourceModel struct {
	Results []RoleBindingSummaryModel `tfsdk:"results"`
}

func (d *RoleBindingsDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_role_bindings"
}

func (d *RoleBindingsDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Lists every role binding configured on the SSPI appliance.\n\n" +
			"Corresponds to `GET /sspi/iam/role-bindings`, walking every page of results. Returns an empty " +
			"list (not an error) if no LDAP identity source is configured yet — the API's own `204` response " +
			"for that case is treated as zero results.",
		Attributes: map[string]schema.Attribute{
			"results": schema.ListNestedAttribute{
				MarkdownDescription: "The role bindings configured on the SSPI appliance.",
				Computed:            true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id":          schema.StringAttribute{Computed: true, MarkdownDescription: "Unique identifier of the role binding."},
						"entity_name": schema.StringAttribute{Computed: true, MarkdownDescription: "The user or group granted these roles."},
						"user_type":   schema.StringAttribute{Computed: true, MarkdownDescription: "`LOCAL_USER`, `REMOTE_USER`, or `REMOTE_GROUP`."},
						"roles": schema.ListAttribute{
							Computed:            true,
							ElementType:         types.StringType,
							MarkdownDescription: "The roles granted to this entity.",
						},
					},
				},
			},
		},
	}
}

func (d *RoleBindingsDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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

func (d *RoleBindingsDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data RoleBindingsDataSourceModel

	tflog.Debug(ctx, "Reading SSPI Role Bindings Data Source")

	bindings, err := pagination.FetchAll(func(offset int) ([]iam_client.RoleBinding, int, error) {
		o := offset
		readResp, err := d.client.GetAllRoleBindingsWithResponse(ctx, &iam_client.GetAllRoleBindingsParams{Offset: &o})
		if err != nil {
			return nil, 0, err
		}
		// A 204 means no role bindings exist because no LDAP identity source
		// is configured yet (documented API behavior) — treat as an empty,
		// final page regardless of which offset it's observed at.
		if readResp.StatusCode() == http.StatusNoContent {
			return nil, 0, nil
		}
		if readResp.StatusCode() != http.StatusOK || readResp.JSON200 == nil {
			return nil, 0, fmt.Errorf("unexpected response status code: %d", readResp.StatusCode())
		}
		var page []iam_client.RoleBinding
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
		resp.Diagnostics.AddError("Error Reading SSPI Role Bindings Data Source", "Could not list role bindings: "+err.Error())
		return
	}

	for _, rb := range bindings {
		item := RoleBindingSummaryModel{
			EntityName: types.StringValue(rb.EntityName),
		}
		if rb.Id != nil {
			item.ID = types.StringValue(*rb.Id)
		}
		if rb.UserType != nil {
			item.UserType = types.StringValue(string(*rb.UserType))
		}
		for _, ro := range rb.Roles {
			item.Roles = append(item.Roles, types.StringValue(string(ro.Role)))
		}
		data.Results = append(data.Results, item)
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
