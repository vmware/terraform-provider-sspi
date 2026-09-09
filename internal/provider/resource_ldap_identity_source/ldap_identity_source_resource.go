// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.

package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/vmware/terraform-provider-sspi/internal/client/iam_client"
)

// Ensure the implementation satisfies the expected interfaces.
var (
	_ resource.Resource                = &LdapIdentitySourceResource{}
	_ resource.ResourceWithConfigure   = &LdapIdentitySourceResource{}
	_ resource.ResourceWithImportState = &LdapIdentitySourceResource{}
)

func Ptr[T any](v T) *T {
	return &v
}

// NewLdapIdentitySourceResource is a helper function to simplify the provider implementation.
func NewLdapIdentitySourceResource() resource.Resource {
	return &LdapIdentitySourceResource{}
}

// LdapIdentitySourceResource is the resource implementation.
type LdapIdentitySourceResource struct {
	client *iam_client.ClientWithResponses
}

// LdapIdentitySourceResourceModel describes the resource data model.
type LdapIdentitySourceResourceModel struct {
	ID        types.String `tfsdk:"id"`
	Domain    types.String `tfsdk:"domain"`
	Server    types.String `tfsdk:"server"`
	Port      types.Int64  `tfsdk:"port"`
	AdminDN   types.String `tfsdk:"admin_dn"`
	Password  types.String `tfsdk:"password"`
	BaseDN    types.String `tfsdk:"base_dn"`
	VerifySSL types.Bool   `tfsdk:"verify_ssl"`
}

func (r *LdapIdentitySourceResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_ldap_identity_source"
}

func (r *LdapIdentitySourceResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages an LDAP Identity Source for the Security Service Platform.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier for the LDAP identity source.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"domain": schema.StringAttribute{
				MarkdownDescription: "The domain name of the LDAP directory (e.g. broadcom.com).",
				Required:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"server": schema.StringAttribute{
				MarkdownDescription: "The IP address or FQDN of the LDAP server.",
				Required:            true,
			},
			"port": schema.Int64Attribute{
				MarkdownDescription: "The port number of the LDAP server.",
				Optional:            true,
			},
			"admin_dn": schema.StringAttribute{
				MarkdownDescription: "The Distinguished Name of the LDAP administrator.",
				Required:            true,
			},
			"password": schema.StringAttribute{
				MarkdownDescription: "The password for the LDAP administrator.",
				Required:            true,
				Sensitive:           true,
			},
			"base_dn": schema.StringAttribute{
				MarkdownDescription: "The Base Distinguished Name for LDAP searches.",
				Required:            true,
			},
			"verify_ssl": schema.BoolAttribute{
				MarkdownDescription: "Not currently supported: `apis/sspi/sspi-iam.yaml`'s `LdapIdentitySourceServer` " +
					"schema has no SSL-verification field (only a `certificates` trust-anchor list), so this " +
					"attribute is not sent to the API and has no effect. Retained only for schema compatibility.",
				Optional: true,
			},
		},
	}
}

func (r *LdapIdentitySourceResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *LdapIdentitySourceResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data LdapIdentitySourceResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, "Creating SSPI LDAP Identity Source")

	port := int64(636)
	if !data.Port.IsNull() {
		port = data.Port.ValueInt64()
	}
	serverURL := fmt.Sprintf("ldaps://%s:%d", data.Server.ValueString(), port)

	body := iam_client.LdapIdentitySource{
		BaseDistinguishedName: data.BaseDN.ValueString(),
		DomainName:            data.Domain.ValueString(),
		LdapType:              iam_client.ACTIVEDIRECTORY,
		LdapServer: iam_client.LdapIdentitySourceServer{
			Url:          serverURL,
			BindIdentity: Ptr(data.AdminDN.ValueString()),
			Password:     Ptr(data.Password.ValueString()),
			Enabled:      Ptr(true),
		},
	}

	createResp, err := r.client.CreateLdapIdentitySourceWithResponse(ctx, body)
	if err != nil {
		resp.Diagnostics.AddError("Error creating LDAP Identity Source", "Could not create LDAP Identity Source: "+err.Error())
		return
	}

	if createResp.StatusCode() != http.StatusOK && createResp.StatusCode() != http.StatusCreated && createResp.StatusCode() != http.StatusAccepted {
		resp.Diagnostics.AddError("Error creating LDAP Identity Source", fmt.Sprintf("Unexpected API response: %d", createResp.StatusCode()))
		return
	}

	if createResp.JSON200 != nil && createResp.JSON200.Id != nil {
		data.ID = types.StringValue(*createResp.JSON200.Id)
	} else {
		data.ID = types.StringValue(data.Domain.ValueString())
	}

	if createResp.JSON200 != nil {
		addConnectivityDiagnostics(&resp.Diagnostics, createResp.JSON200.ConnectivityResult)
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// addConnectivityDiagnostics surfaces a hard error if the API's embedded
// connectivity probe failed, instead of letting Create/Update silently report
// success for an LDAP configuration that cannot actually authenticate.
func addConnectivityDiagnostics(diags *diag.Diagnostics, result *iam_client.LdapIdentitySourceConnectivityResult) {
	if result == nil || result.Result != iam_client.FAILURE {
		return
	}
	detail := "The LDAP server connectivity probe failed."
	if result.Errors != nil {
		for _, e := range *result.Errors {
			detail += fmt.Sprintf("\n  - %s: %s", e.ErrorType, e.Message)
		}
	}
	diags.AddError("LDAP Identity Source Connectivity Check Failed", detail)
}

func (r *LdapIdentitySourceResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data LdapIdentitySourceResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, "Reading SSPI LDAP Identity Source", map[string]interface{}{"id": data.ID.ValueString()})

	readResp, err := r.client.GetLdapIdentitySourceWithResponse(ctx, data.ID.ValueString(), &iam_client.GetLdapIdentitySourceParams{})
	if err != nil {
		resp.Diagnostics.AddError(
			"Error reading SSPI LDAP Identity Source",
			"Could not read SSPI LDAP Identity Source: "+err.Error(),
		)
		return
	}

	if readResp.StatusCode() == http.StatusNotFound {
		resp.State.RemoveResource(ctx)
		return
	}

	if readResp.JSON200 == nil {
		resp.Diagnostics.AddError(
			"Error reading SSPI LDAP Identity Source",
			fmt.Sprintf("Unexpected response from API: %d, body: %s", readResp.StatusCode(), string(readResp.Body)),
		)
		return
	}

	src := readResp.JSON200
	data.Domain = types.StringValue(src.DomainName)
	data.BaseDN = types.StringValue(src.BaseDistinguishedName)
	if src.LdapServer.BindIdentity != nil {
		data.AdminDN = types.StringValue(*src.LdapServer.BindIdentity)
	} else {
		data.AdminDN = types.StringNull()
	}
	if server, port, ok := splitLdapServerURL(src.LdapServer.Url); ok {
		data.Server = types.StringValue(server)
		data.Port = types.Int64Value(port)
	}
	// verify_ssl has no server-side equivalent (see schema description) and is
	// intentionally left as-is from prior state/config rather than overwritten.

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// splitLdapServerURL parses the "ldaps://host:port" URL the API returns for
// LdapServer.Url back into its host and port components, mirroring the
// fmt.Sprintf("ldaps://%s:%d", ...) construction used in Create/Update.
func splitLdapServerURL(raw string) (host string, port int64, ok bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return "", 0, false
	}
	p, err := strconv.ParseInt(u.Port(), 10, 64)
	if err != nil {
		return "", 0, false
	}
	return u.Hostname(), p, true
}

func (r *LdapIdentitySourceResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var data LdapIdentitySourceResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, "Updating SSPI LDAP Identity Source", map[string]interface{}{"id": data.ID.ValueString()})

	port := int64(636)
	if !data.Port.IsNull() {
		port = data.Port.ValueInt64()
	}
	serverURL := fmt.Sprintf("ldaps://%s:%d", data.Server.ValueString(), port)

	body := iam_client.LdapIdentitySource{
		BaseDistinguishedName: data.BaseDN.ValueString(),
		DomainName:            data.Domain.ValueString(),
		LdapType:              iam_client.ACTIVEDIRECTORY,
		LdapServer: iam_client.LdapIdentitySourceServer{
			Url:          serverURL,
			BindIdentity: Ptr(data.AdminDN.ValueString()),
			Password:     Ptr(data.Password.ValueString()),
			Enabled:      Ptr(true),
		},
	}

	updateResp, err := r.client.UpdateLdapIdentitySourceWithResponse(ctx, data.ID.ValueString(), body)
	if err != nil {
		resp.Diagnostics.AddError("Error updating LDAP Identity Source", "Could not update LDAP Identity Source: "+err.Error())
		return
	}

	if updateResp.StatusCode() != http.StatusOK && updateResp.StatusCode() != http.StatusAccepted {
		resp.Diagnostics.AddError("Error updating LDAP Identity Source", fmt.Sprintf("Unexpected API response: %d", updateResp.StatusCode()))
		return
	}

	if updateResp.JSON200 != nil {
		addConnectivityDiagnostics(&resp.Diagnostics, updateResp.JSON200.ConnectivityResult)
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *LdapIdentitySourceResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data LdapIdentitySourceResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, "Deleting SSPI LDAP Identity Source", map[string]interface{}{"id": data.ID.ValueString()})

	deleteResp, err := r.client.DeleteLdapIdentitySourceWithResponse(ctx, data.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(
			"Error deleting SSPI LDAP Identity Source",
			"Could not delete SSPI LDAP Identity Source: "+err.Error(),
		)
		return
	}

	if deleteResp.StatusCode() != http.StatusOK && deleteResp.StatusCode() != http.StatusNoContent && deleteResp.StatusCode() != http.StatusNotFound {
		resp.Diagnostics.AddError(
			"Error deleting SSPI LDAP Identity Source",
			fmt.Sprintf("Unexpected response from API: %d", deleteResp.StatusCode()),
		)
		return
	}
}

func (r *LdapIdentitySourceResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
