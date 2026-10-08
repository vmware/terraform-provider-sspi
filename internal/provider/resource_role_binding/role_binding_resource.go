// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.

package provider

import (
	"context"
	"fmt"
	"net/http"

	"github.com/hashicorp/terraform-plugin-framework-validators/setvalidator"
	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/vmware/terraform-provider-sspi/internal/client/iam_client"
)

var (
	_ resource.Resource                = &RoleBindingResource{}
	_ resource.ResourceWithConfigure   = &RoleBindingResource{}
	_ resource.ResourceWithImportState = &RoleBindingResource{}
)

func NewRoleBindingResource() resource.Resource {
	return &RoleBindingResource{}
}

type RoleBindingResource struct {
	client *iam_client.ClientWithResponses
}

type RoleBindingResourceModel struct {
	ID         types.String `tfsdk:"id"`
	EntityName types.String `tfsdk:"entity_name"`
	UserType   types.String `tfsdk:"user_type"`
	Roles      types.Set    `tfsdk:"roles"`
}

func (r *RoleBindingResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_role_binding"
}

func (r *RoleBindingResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Grants SSPI roles to a remote (LDAP) user or remote (LDAP) group. " +
			"Local users cannot be granted roles through this API.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier of the role binding.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"entity_name": schema.StringAttribute{
				MarkdownDescription: "The remote user or group being granted roles (for example `Administrators` or " +
					"`sspuser1@corp.example.com`). Changing this forces a new role binding.",
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"user_type": schema.StringAttribute{
				MarkdownDescription: "The kind of entity: `REMOTE_USER` or `REMOTE_GROUP`. Changing this forces a new role binding.",
				Required:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
				Validators: []validator.String{
					stringvalidator.OneOf(string(iam_client.REMOTEUSER), string(iam_client.REMOTEGROUP)),
				},
			},
			"roles": schema.SetAttribute{
				MarkdownDescription: "The roles to grant: `ENTERPRISE_ADMIN`, `AUDITOR`, and/or `SUPPORT_BUNDLE_COLLECTOR`. " +
					"On update this set fully replaces the entity's existing roles.",
				ElementType: types.StringType,
				Required:    true,
				Validators: []validator.Set{
					setvalidator.SizeAtLeast(1),
					setvalidator.ValueStringsAre(stringvalidator.OneOf(
						string(iam_client.ENTERPRISEADMIN),
						string(iam_client.AUDITOR),
						string(iam_client.SUPPORTBUNDLECOLLECTOR),
					)),
				},
			},
		},
	}
}

func (r *RoleBindingResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	clientProvider, ok := req.ProviderData.(interface {
		GetIAM() *iam_client.ClientWithResponses
	})
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Resource Configure Type",
			fmt.Sprintf("Expected provider data with GetIAM(), got: %T. Please report this issue to the provider developers.", req.ProviderData),
		)
		return
	}

	r.client = clientProvider.GetIAM()
	if r.client == nil {
		resp.Diagnostics.AddError(
			"SSPI Appliance Not Configured",
			"This resource requires the SSPI appliance connection to be configured on the provider "+
				"(\"sspi_host\", \"sspi_username\", and \"sspi_password\", or the SSPI_HOST, SSPI_USERNAME, "+
				"and SSPI_PASSWORD environment variables).",
		)
	}
}

func rolesFromSet(ctx context.Context, s types.Set) ([]iam_client.Role, diag.Diagnostics) {
	var names []string
	diags := s.ElementsAs(ctx, &names, false)
	if diags.HasError() {
		return nil, diags
	}
	roles := make([]iam_client.Role, 0, len(names))
	for _, n := range names {
		roles = append(roles, iam_client.Role{Role: iam_client.SspInstallerRole(n)})
	}
	return roles, diags
}

func rolesToSet(ctx context.Context, roles []iam_client.Role) (types.Set, diag.Diagnostics) {
	names := make([]string, 0, len(roles))
	for _, role := range roles {
		names = append(names, string(role.Role))
	}
	return types.SetValueFrom(ctx, types.StringType, names)
}

func (r *RoleBindingResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data RoleBindingResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	roles, diags := rolesFromSet(ctx, data.Roles)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, "Creating SSPI Role Binding", map[string]interface{}{"entity_name": data.EntityName.ValueString()})

	userType := iam_client.UserType(data.UserType.ValueString())
	createResp, err := r.client.BindRolesToEntityWithResponse(ctx, iam_client.RoleBinding{
		EntityName: data.EntityName.ValueString(),
		UserType:   &userType,
		Roles:      roles,
	})
	if err != nil {
		resp.Diagnostics.AddError("Error creating Role Binding", "Could not create Role Binding: "+err.Error())
		return
	}

	if createResp.StatusCode() != http.StatusOK && createResp.StatusCode() != http.StatusCreated && createResp.StatusCode() != http.StatusAccepted {
		resp.Diagnostics.AddError("Error creating Role Binding", fmt.Sprintf("Unexpected API response: %d, body: %s", createResp.StatusCode(), string(createResp.Body)))
		return
	}

	if createResp.JSON200 == nil || createResp.JSON200.Id == nil {
		resp.Diagnostics.AddError("Error creating Role Binding", fmt.Sprintf("API did not return the new role binding id, body: %s", string(createResp.Body)))
		return
	}
	data.ID = types.StringValue(*createResp.JSON200.Id)

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *RoleBindingResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data RoleBindingResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, "Reading SSPI Role Binding", map[string]interface{}{"id": data.ID.ValueString()})

	readResp, err := r.client.GetRoleBindingWithResponse(ctx, data.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Error reading Role Binding", "Could not read Role Binding: "+err.Error())
		return
	}

	if readResp.StatusCode() == http.StatusNotFound {
		resp.State.RemoveResource(ctx)
		return
	}

	if readResp.JSON200 == nil {
		resp.Diagnostics.AddError("Error reading Role Binding", fmt.Sprintf("Unexpected response from API: %d, body: %s", readResp.StatusCode(), string(readResp.Body)))
		return
	}

	binding := readResp.JSON200
	data.EntityName = types.StringValue(binding.EntityName)
	if binding.UserType != nil {
		data.UserType = types.StringValue(string(*binding.UserType))
	}
	roles, diags := rolesToSet(ctx, binding.Roles)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}
	data.Roles = roles

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *RoleBindingResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var plan, state RoleBindingResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &plan)...)
	resp.Diagnostics.Append(req.State.Get(ctx, &state)...)
	if resp.Diagnostics.HasError() {
		return
	}

	roles, diags := rolesFromSet(ctx, plan.Roles)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	id := state.ID.ValueString()
	tflog.Debug(ctx, "Updating SSPI Role Binding", map[string]interface{}{"id": id})

	// The PUT is rejected without the current _revision (428), or with a stale one (412).
	currentResp, err := r.client.GetRoleBindingWithResponse(ctx, id)
	if err != nil {
		resp.Diagnostics.AddError("Error reading current Role Binding", err.Error())
		return
	}
	if currentResp.JSON200 == nil {
		resp.Diagnostics.AddError("Error reading current Role Binding", fmt.Sprintf("Unexpected response from API: %d, body: %s", currentResp.StatusCode(), string(currentResp.Body)))
		return
	}

	userType := iam_client.UserType(plan.UserType.ValueString())
	updateResp, err := r.client.UpdateRolesToEntityWithResponse(ctx, id, iam_client.RoleBinding{
		EntityName:         plan.EntityName.ValueString(),
		UserType:           &userType,
		Roles:              roles,
		UnderscoreRevision: currentResp.JSON200.UnderscoreRevision,
	})
	if err != nil {
		resp.Diagnostics.AddError("Error updating Role Binding", "Could not update Role Binding: "+err.Error())
		return
	}

	if updateResp.StatusCode() != http.StatusOK && updateResp.StatusCode() != http.StatusAccepted {
		resp.Diagnostics.AddError("Error updating Role Binding", fmt.Sprintf("Unexpected API response: %d, body: %s", updateResp.StatusCode(), string(updateResp.Body)))
		return
	}

	plan.ID = state.ID
	resp.Diagnostics.Append(resp.State.Set(ctx, &plan)...)
}

func (r *RoleBindingResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data RoleBindingResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, "Deleting SSPI Role Binding", map[string]interface{}{"id": data.ID.ValueString()})

	deleteResp, err := r.client.DeleteRolesToEntityWithResponse(ctx, data.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Error deleting Role Binding", "Could not delete Role Binding: "+err.Error())
		return
	}

	if deleteResp.StatusCode() != http.StatusOK && deleteResp.StatusCode() != http.StatusNoContent && deleteResp.StatusCode() != http.StatusNotFound {
		resp.Diagnostics.AddError("Error deleting Role Binding", fmt.Sprintf("Unexpected response from API: %d, body: %s", deleteResp.StatusCode(), string(deleteResp.Body)))
	}
}

func (r *RoleBindingResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
