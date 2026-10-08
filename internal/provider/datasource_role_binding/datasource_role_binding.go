// © Broadcom 2026. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.

package provider

import (
	"context"
	"fmt"
	"net/http"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/vmware/terraform-provider-sspi/internal/client/iam_client"
)

const listPageSize = 100

var (
	_ datasource.DataSource              = &RoleBindingDataSource{}
	_ datasource.DataSourceWithConfigure = &RoleBindingDataSource{}
)

func NewRoleBindingDataSource() datasource.DataSource {
	return &RoleBindingDataSource{}
}

type RoleBindingDataSource struct {
	client *iam_client.ClientWithResponses
}

type RoleBindingDataSourceModel struct {
	ID         types.String `tfsdk:"id"`
	EntityName types.String `tfsdk:"entity_name"`
	UserType   types.String `tfsdk:"user_type"`
	Roles      types.Set    `tfsdk:"roles"`
}

func (d *RoleBindingDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_role_binding"
}

func (d *RoleBindingDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Fetches an SSPI role binding, either by its `id` or by the exact `entity_name` " +
			"(optionally narrowed with `user_type`) it was granted to.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier of the role binding. Exactly one of `id` or `entity_name` must be set.",
				Optional:            true,
				Computed:            true,
				Validators: []validator.String{
					stringvalidator.ExactlyOneOf(path.MatchRoot("entity_name")),
				},
			},
			"entity_name": schema.StringAttribute{
				MarkdownDescription: "Exact name of the user or group the roles were granted to. Exactly one of `id` or `entity_name` must be set.",
				Optional:            true,
				Computed:            true,
			},
			"user_type": schema.StringAttribute{
				MarkdownDescription: "The kind of entity: `LOCAL_USER`, `REMOTE_USER` or `REMOTE_GROUP`. When looking up by " +
					"`entity_name`, set this to disambiguate if the same name exists with more than one type.",
				Optional: true,
				Computed: true,
				Validators: []validator.String{
					stringvalidator.OneOf(string(iam_client.LOCALUSER), string(iam_client.REMOTEUSER), string(iam_client.REMOTEGROUP)),
				},
			},
			"roles": schema.SetAttribute{
				MarkdownDescription: "The roles held by the entity.",
				ElementType:         types.StringType,
				Computed:            true,
			},
		},
	}
}

func (d *RoleBindingDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
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
	}
}

func (d *RoleBindingDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data RoleBindingDataSourceModel

	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var binding *iam_client.RoleBinding
	if !data.ID.IsNull() {
		tflog.Debug(ctx, "Reading SSPI Role Binding Data Source by id", map[string]interface{}{"id": data.ID.ValueString()})
		readResp, err := d.client.GetRoleBindingWithResponse(ctx, data.ID.ValueString())
		if err != nil {
			resp.Diagnostics.AddError("Error Reading SSPI Role Binding Data Source", "Could not read role binding: "+err.Error())
			return
		}
		if readResp.StatusCode() == http.StatusNotFound {
			resp.Diagnostics.AddError("Role Binding Not Found", fmt.Sprintf("No role binding with id %q exists.", data.ID.ValueString()))
			return
		}
		if readResp.JSON200 == nil {
			resp.Diagnostics.AddError("Error Reading SSPI Role Binding Data Source", fmt.Sprintf("Unexpected response from API: %d, body: %s", readResp.StatusCode(), string(readResp.Body)))
			return
		}
		binding = readResp.JSON200
	} else {
		var err error
		binding, err = d.findByEntityName(ctx, data, resp)
		if err != nil || resp.Diagnostics.HasError() {
			return
		}
	}

	if binding.Id == nil {
		resp.Diagnostics.AddError("Error Reading SSPI Role Binding Data Source", "The API returned a role binding without an id.")
		return
	}
	data.ID = types.StringValue(*binding.Id)
	data.EntityName = types.StringValue(binding.EntityName)
	if binding.UserType != nil {
		data.UserType = types.StringValue(string(*binding.UserType))
	} else {
		data.UserType = types.StringNull()
	}

	names := make([]string, 0, len(binding.Roles))
	for _, role := range binding.Roles {
		names = append(names, string(role.Role))
	}
	roles, diags := types.SetValueFrom(ctx, types.StringType, names)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	data.Roles = roles

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// findByEntityName pages through GET /iam/role-bindings and returns the single
// binding whose entity_name (and user_type, when set) matches exactly. The API's
// user_name filter is a "contains" match, so the exact comparison is done here.
func (d *RoleBindingDataSource) findByEntityName(ctx context.Context, data RoleBindingDataSourceModel, resp *datasource.ReadResponse) (*iam_client.RoleBinding, error) {
	name := data.EntityName.ValueString()
	tflog.Debug(ctx, "Reading SSPI Role Binding Data Source by entity_name", map[string]interface{}{"entity_name": name})

	var matches []iam_client.RoleBinding
	pageSize := int32(listPageSize)
	for offset := 0; ; offset += listPageSize {
		off := offset
		listResp, err := d.client.GetAllRoleBindingsWithResponse(ctx, &iam_client.GetAllRoleBindingsParams{
			UserName: &name,
			Offset:   &off,
			PageSize: &pageSize,
		})
		if err != nil {
			resp.Diagnostics.AddError("Error Reading SSPI Role Binding Data Source", "Could not list role bindings: "+err.Error())
			return nil, err
		}
		if listResp.JSON200 == nil {
			resp.Diagnostics.AddError("Error Reading SSPI Role Binding Data Source", fmt.Sprintf("Unexpected response from API: %d, body: %s", listResp.StatusCode(), string(listResp.Body)))
			return nil, fmt.Errorf("unexpected status %d", listResp.StatusCode())
		}

		var page []iam_client.RoleBinding
		if listResp.JSON200.Results != nil {
			page = *listResp.JSON200.Results
		}
		for _, b := range page {
			if b.EntityName != name {
				continue
			}
			if !data.UserType.IsNull() && !data.UserType.IsUnknown() && (b.UserType == nil || string(*b.UserType) != data.UserType.ValueString()) {
				continue
			}
			matches = append(matches, b)
		}
		if len(page) < listPageSize {
			break
		}
	}

	switch len(matches) {
	case 0:
		resp.Diagnostics.AddError("Role Binding Not Found", fmt.Sprintf("No role binding found for entity_name %q.", name))
		return nil, nil
	case 1:
		return &matches[0], nil
	default:
		resp.Diagnostics.AddError("Multiple Role Bindings Found", fmt.Sprintf("%d role bindings match entity_name %q; set user_type to select one.", len(matches), name))
		return nil, nil
	}
}
