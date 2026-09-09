// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.

package provider

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/vmware/terraform-provider-sspi/internal/client/api_client"
)

// Ensure provider defined types fully satisfy framework interfaces.
var _ resource.Resource = &ProviderResource{}
var _ resource.ResourceWithImportState = &ProviderResource{}

func NewProviderResource() resource.Resource {
	return &ProviderResource{}
}

// ProviderResource defines the resource implementation.
type ProviderResource struct {
	client *api_client.ClientWithResponses
}

// ProviderResourceModel describes the resource data model.
type ProviderResourceModel struct {
	ID          types.String `tfsdk:"id"`
	Server      types.String `tfsdk:"server"`
	User        types.String `tfsdk:"user"`
	Password    types.String `tfsdk:"password"`
	Certificate types.String `tfsdk:"certificate"`
	CreatedAt   types.String `tfsdk:"created_at"`
	UpdatedAt   types.String `tfsdk:"updated_at"`
}

func (r *ProviderResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_vsphere_provider"
}

func (r *ProviderResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a vSphere Provider connection in the SSPI.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier for the vSphere provider.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"server": schema.StringAttribute{
				MarkdownDescription: "The IP address or FQDN of the vCenter Server.",
				Required:            true,
			},
			"user": schema.StringAttribute{
				MarkdownDescription: "The username used to connect to vCenter (e.g. administrator@vsphere.local).",
				Required:            true,
			},
			"password": schema.StringAttribute{
				MarkdownDescription: "The password for the vCenter user.",
				Required:            true,
				Sensitive:           true,
			},
			"certificate": schema.StringAttribute{
				MarkdownDescription: "The full PEM-encoded TLS certificate of the vCenter Server " +
					"(`-----BEGIN CERTIFICATE-----...-----END CERTIFICATE-----`), not a SHA thumbprint/fingerprint " +
					"— confirmed against `apis/sspi/sspi-api.yaml`'s `Provider.certificate` schema and live API " +
					"behavior, which rejects a bare thumbprint with a govc \"cannot be used as a trusted CA " +
					"certificate\" error.",
				Required: true,
			},
			"created_at": schema.StringAttribute{
				MarkdownDescription: "Resource creation time.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"updated_at": schema.StringAttribute{
				MarkdownDescription: "Resource last update time.",
				Computed:            true,
			},
		},
	}
}

func (r *ProviderResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	clientProvider, ok := req.ProviderData.(interface {
		GetAPI() *api_client.ClientWithResponses
	})

	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Resource Configure Type",
			fmt.Sprintf("Expected provider data with GetAPI(), got: %T. Please report this issue to the provider developers.", req.ProviderData),
		)
		return
	}

	r.client = clientProvider.GetAPI()
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

func Ptr[T any](v T) *T {
	return &v
}

func (r *ProviderResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data ProviderResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	var cert *string
	if v := data.Certificate.ValueString(); v != "" {
		cert = &v
	}

	createReq := api_client.VsphereProviderDefinition{
		Server:      data.Server.ValueString(),
		User:        data.User.ValueString(),
		Password:    Ptr(data.Password.ValueString()),
		Certificate: cert,
	}

	tflog.Debug(ctx, "Creating SSPI Provider")

	createResp, err := r.client.CreateVsphereProviderWithResponse(ctx, createReq)
	if err != nil {
		resp.Diagnostics.AddError(
			"Error creating SSPI Provider",
			"Could not create SSPI Provider, unexpected error: "+err.Error(),
		)
		return
	}

	if createResp.JSON200 == nil || createResp.JSON200.Id == nil {
		resp.Diagnostics.AddError(
			"Error creating SSPI Provider",
			fmt.Sprintf("Unexpected response from API: %d, body: %s", createResp.StatusCode(), string(createResp.Body)),
		)
		return
	}

	data.ID = types.StringValue(*createResp.JSON200.Id)

	// Fetch full state from SSPI
	found := r.readProvider(ctx, data.ID.ValueString(), &data, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if !found {
		resp.Diagnostics.AddError(
			"Error creating SSPI Provider",
			"SSPI Provider was created but could not be found immediately afterward.",
		)
		return
	}

	tflog.Trace(ctx, "Created SSPI Provider", map[string]interface{}{"id": data.ID.ValueString()})
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *ProviderResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data ProviderResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	found := r.readProvider(ctx, data.ID.ValueString(), &data, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if !found {
		resp.State.RemoveResource(ctx)
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// readProvider reads the vSphere provider into data, returning false (with no
// error) if the provider no longer exists (HTTP 404), so callers can decide
// whether that means "drop it from state" (Read) or "unexpected" (Create/Update).
func (r *ProviderResource) readProvider(ctx context.Context, id string, data *ProviderResourceModel, diags *diag.Diagnostics) bool {
	tflog.Debug(ctx, "Reading SSPI Provider", map[string]interface{}{"id": id})

	readResp, err := r.client.GetVsphereProviderWithResponse(ctx, id)
	if err != nil {
		diags.AddError(
			"Error reading SSPI Provider",
			"Could not read SSPI Provider: "+err.Error(),
		)
		return false
	}

	if readResp.StatusCode() == http.StatusNotFound {
		return false
	}

	if readResp.JSON200 == nil {
		diags.AddError(
			"Error reading SSPI Provider",
			fmt.Sprintf("Unexpected response from API: %d", readResp.StatusCode()),
		)
		return false
	}

	prov := readResp.JSON200
	if prov.Id != nil {
		data.ID = types.StringValue(*prov.Id)
	}
	data.Server = types.StringValue(prov.Server)
	data.User = types.StringValue(prov.User)
	if prov.Certificate != nil {
		data.Certificate = types.StringValue(*prov.Certificate)
	}
	if prov.UnderscoreCreateTime != nil {
		data.CreatedAt = types.StringValue(time.UnixMilli(*prov.UnderscoreCreateTime).UTC().Format(time.RFC3339))
	}
	if prov.UnderscoreLastModifiedTime != nil {
		data.UpdatedAt = types.StringValue(time.UnixMilli(*prov.UnderscoreLastModifiedTime).UTC().Format(time.RFC3339))
	}
	return true
}

func (r *ProviderResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var data ProviderResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	updateReq := api_client.VsphereProviderDefinition{
		Server:      data.Server.ValueString(),
		User:        data.User.ValueString(),
		Password:    Ptr(data.Password.ValueString()),
		Certificate: Ptr(data.Certificate.ValueString()),
	}

	tflog.Debug(ctx, "Updating SSPI Provider", map[string]interface{}{"id": data.ID.ValueString()})

	updateResp, err := r.client.UpdateVsphereProviderWithResponse(ctx, data.ID.ValueString(), updateReq)
	if err != nil {
		resp.Diagnostics.AddError(
			"Error updating SSPI Provider",
			"Could not update SSPI Provider, unexpected error: "+err.Error(),
		)
		return
	}

	if updateResp.StatusCode() != http.StatusOK && updateResp.StatusCode() != http.StatusAccepted {
		resp.Diagnostics.AddError(
			"Error updating SSPI Provider",
			fmt.Sprintf("Unexpected response from API: %d", updateResp.StatusCode()),
		)
		return
	}

	found := r.readProvider(ctx, data.ID.ValueString(), &data, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if !found {
		resp.Diagnostics.AddError(
			"Error updating SSPI Provider",
			"SSPI Provider was updated but could not be found immediately afterward.",
		)
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *ProviderResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data ProviderResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, "Deleting SSPI Provider", map[string]interface{}{"id": data.ID.ValueString()})

	deleteResp, err := r.client.DeleteVsphereProviderWithResponse(ctx, data.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(
			"Error deleting SSPI Provider",
			"Could not delete SSPI Provider, unexpected error: "+err.Error(),
		)
		return
	}

	if deleteResp.StatusCode() != http.StatusOK && deleteResp.StatusCode() != http.StatusNoContent && deleteResp.StatusCode() != http.StatusNotFound {
		resp.Diagnostics.AddError(
			"Error deleting SSPI Provider",
			fmt.Sprintf("Unexpected response from API: %d", deleteResp.StatusCode()),
		)
		return
	}
}

func (r *ProviderResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
