// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.

package resource_user_password

import (
	"context"
	"fmt"
	"net/http"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/vmware/terraform-provider-sspi/internal/client/iam_client"
)

var (
	_ resource.Resource              = &UserPasswordResource{}
	_ resource.ResourceWithConfigure = &UserPasswordResource{}
)

func NewUserPasswordResource() resource.Resource {
	return &UserPasswordResource{}
}

type UserPasswordResource struct {
	client *iam_client.ClientWithResponses
}

type UserPasswordResourceModel struct {
	ID              types.String `tfsdk:"id"`
	Username        types.String `tfsdk:"username"`
	CurrentPassword types.String `tfsdk:"current_password"`
	NewPassword     types.String `tfsdk:"new_password"`
}

func (r *UserPasswordResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_user_password"
}

func (r *UserPasswordResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages SSPI local user passwords.\n\n" +
			"When `username` is not set, changes the password of the currently authenticated user via " +
			"`POST /sspi/iam/change-password` (requires `current_password`).\n\n" +
			"When `username` is set, performs an administrator-driven reset of that local user's password " +
			"(e.g. `admin` or `audit`) via `POST /sspi/iam/reset-password` (`current_password` is not required " +
			"or used for this path).",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier for the password action.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"username": schema.StringAttribute{
				MarkdownDescription: "The local user whose password should be reset by an administrator " +
					"(e.g. `admin`, `audit`). When omitted, the password of the currently authenticated " +
					"user is changed instead.",
				Optional: true,
			},
			"current_password": schema.StringAttribute{
				MarkdownDescription: "Current password of the authenticated user. Required when `username` " +
					"is not set (self password change); ignored for administrator resets.",
				Optional:  true,
				Sensitive: true,
			},
			"new_password": schema.StringAttribute{
				MarkdownDescription: "New password to set.",
				Required:            true,
				Sensitive:           true,
			},
		},
	}
}

func (r *UserPasswordResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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
		return
	}
}

func (r *UserPasswordResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data UserPasswordResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	id := r.applyPassword(ctx, &data, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	data.ID = types.StringValue(id)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *UserPasswordResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var data UserPasswordResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	id := r.applyPassword(ctx, &data, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	data.ID = types.StringValue(id)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// applyPassword performs either an administrator password reset (when username is set) or a
// self-service password change (when username is unset), and returns the resulting resource ID.
func (r *UserPasswordResource) applyPassword(ctx context.Context, data *UserPasswordResourceModel, diags *diag.Diagnostics) string {
	newPwd := data.NewPassword.ValueString()
	username := data.Username.ValueString()

	if username != "" {
		tflog.Debug(ctx, "Resetting SSPI local user password", map[string]any{"username": username})

		body := iam_client.ResetPasswordJSONRequestBody{
			UserName:    username,
			NewPassword: newPwd,
		}

		res, err := r.client.ResetPasswordWithResponse(ctx, body)
		if err != nil {
			diags.AddError("Error resetting password", err.Error())
			return ""
		}

		if res.StatusCode() != http.StatusOK && res.StatusCode() != http.StatusNoContent {
			diags.AddError("Error resetting password", fmt.Sprintf("Unexpected status code: %d, body: %s", res.StatusCode(), string(res.Body)))
			return ""
		}

		return "pwd-reset-" + username
	}

	if data.CurrentPassword.IsNull() || data.CurrentPassword.ValueString() == "" {
		diags.AddError(
			"Missing current_password",
			"current_password is required when username is not set, since this performs a self-service password change for the currently authenticated user.",
		)
		return ""
	}

	tflog.Debug(ctx, "Changing SSPI current user password")

	currentPwd := data.CurrentPassword.ValueString()
	body := iam_client.ChangePasswordJSONRequestBody{
		OldPassword: &currentPwd,
		NewPassword: &newPwd,
	}

	res, err := r.client.ChangePasswordWithResponse(ctx, body)
	if err != nil {
		diags.AddError("Error changing password", err.Error())
		return ""
	}

	if res.StatusCode() != http.StatusOK && res.StatusCode() != http.StatusNoContent {
		diags.AddError("Error changing password", fmt.Sprintf("Unexpected status code: %d, body: %s", res.StatusCode(), string(res.Body)))
		return ""
	}

	return "pwd-change"
}

func (r *UserPasswordResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	// Passwords cannot be read back from the API; preserve the current state as-is.
	var data UserPasswordResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *UserPasswordResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	// Password changes/resets are not reverted on destroy.
}
