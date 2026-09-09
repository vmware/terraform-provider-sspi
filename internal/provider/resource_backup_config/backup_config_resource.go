// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.

package provider

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/vmware/terraform-provider-sspi/internal/client/api_client"
)

// Ensure the implementation satisfies the expected interfaces.
var (
	_ resource.Resource                = &BackupConfigResource{}
	_ resource.ResourceWithConfigure   = &BackupConfigResource{}
	_ resource.ResourceWithImportState = &BackupConfigResource{}
)

func NewBackupConfigResource() resource.Resource {
	return &BackupConfigResource{}
}

type BackupConfigResource struct {
	client *api_client.ClientWithResponses
}

type BackupConfigResourceModel struct {
	ID             types.String `tfsdk:"id"`
	ServerAddress  types.String `tfsdk:"server_address"`
	Protocol       types.String `tfsdk:"protocol"`
	Port           types.Int64  `tfsdk:"port"`
	Username       types.String `tfsdk:"username"`
	Password       types.String `tfsdk:"password"`
	SSHPublicKey   types.String `tfsdk:"ssh_public_key"`
	BackupLocation types.String `tfsdk:"backup_location"`
	Passphrase     types.String `tfsdk:"passphrase"`
}

func (r *BackupConfigResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_installer_backup_config"
}

func (r *BackupConfigResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages the SFTP remote backup target configuration for SSPI.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier for the backup configuration (always 'singleton').",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"server_address": schema.StringAttribute{
				MarkdownDescription: "The SFTP server IP or hostname.",
				Required:            true,
			},
			"protocol": schema.StringAttribute{
				MarkdownDescription: "The backup transfer protocol (e.g. SFTP).",
				Optional:            true,
			},
			"port": schema.Int64Attribute{
				MarkdownDescription: "The SFTP server port.",
				Optional:            true,
			},
			"username": schema.StringAttribute{
				MarkdownDescription: "The username for SFTP authentication.",
				Required:            true,
			},
			"password": schema.StringAttribute{
				MarkdownDescription: "The password for SFTP authentication.",
				Required:            true,
				Sensitive:           true,
			},
			"ssh_public_key": schema.StringAttribute{
				MarkdownDescription: "The SSH public host key of the SFTP server.",
				Optional:            true,
			},
			"backup_location": schema.StringAttribute{
				MarkdownDescription: "Target directory path on the remote SFTP server. Must not end in a " +
					"trailing slash (rejected at plan time) — the API strips one if present, which would " +
					"otherwise cause a permanent post-apply diff.",
				Required:   true,
				Validators: []validator.String{noTrailingSlash()},
			},
			"passphrase": schema.StringAttribute{
				MarkdownDescription: "Passphrase to secure the backup bundle file. Not listed in " +
					"`apis/sspi/sspi-api.yaml`'s `BackupConfig` schema at all, but the live " +
					"`PUT /sspi/backup/config` API rejects requests without one " +
					"(`\"Passphrase can not be empty.\"`) — required here to match observed live behavior.",
				Required:  true,
				Sensitive: true,
			},
		},
	}
}

func (r *BackupConfigResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *BackupConfigResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data BackupConfigResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, "Creating SSPI Backup Configuration")

	r.saveBackupConfig(ctx, &data, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	data.ID = types.StringValue("singleton")
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *BackupConfigResource) saveBackupConfig(ctx context.Context, data *BackupConfigResourceModel, diags *diag.Diagnostics) {
	port := 22
	if !data.Port.IsNull() {
		port = int(data.Port.ValueInt64())
	}

	proto := api_client.SFTP
	if !data.Protocol.IsNull() {
		proto = api_client.BackupConfigProtocol(data.Protocol.ValueString())
	}

	sshKey := ""
	if !data.SSHPublicKey.IsNull() {
		sshKey = data.SSHPublicKey.ValueString()
	}

	pwd := data.Password.ValueString()
	passphrase := data.Passphrase.ValueString()

	body := api_client.BackupConfig{
		BackupLocation: data.BackupLocation.ValueString(),
		Password:       &pwd,
		Passphrase:     &passphrase,
		Port:           port,
		Protocol:       proto,
		ServerAddress:  data.ServerAddress.ValueString(),
		SshPublicKey:   sshKey,
		Username:       data.Username.ValueString(),
	}

	updateResp, err := r.client.UpdateBackupConfigWithResponse(ctx, body)
	if err != nil {
		diags.AddError("Error saving SSPI Backup Configuration", "Could not save backup configuration: "+err.Error())
		return
	}

	if updateResp.StatusCode() != http.StatusOK && updateResp.StatusCode() != http.StatusAccepted {
		diags.AddError("Error saving SSPI Backup Configuration",
			fmt.Sprintf("Unexpected API response: %d: %s", updateResp.StatusCode(), string(updateResp.Body)))
		return
	}
}

func (r *BackupConfigResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data BackupConfigResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, "Reading SSPI Backup Configuration")

	readResp, err := r.client.GetBackupConfigWithResponse(ctx)
	if err != nil {
		resp.Diagnostics.AddError(
			"Error reading SSPI Backup Configuration",
			"Could not read SSPI Backup Configuration, unexpected error: "+err.Error(),
		)
		return
	}

	if readResp.StatusCode() == http.StatusNotFound {
		resp.State.RemoveResource(ctx)
		return
	}

	if readResp.JSON200 == nil {
		resp.Diagnostics.AddError(
			"Error reading SSPI Backup Configuration",
			fmt.Sprintf("Unexpected response from API: %d, body: %s", readResp.StatusCode(), string(readResp.Body)),
		)
		return
	}

	cfg := readResp.JSON200
	data.ServerAddress = types.StringValue(cfg.ServerAddress)
	data.Username = types.StringValue(cfg.Username)
	data.BackupLocation = types.StringValue(strings.TrimRight(cfg.BackupLocation, "/"))
	data.Port = types.Int64Value(int64(cfg.Port))
	data.Protocol = types.StringValue(string(cfg.Protocol))
	if cfg.SshPublicKey != "" {
		data.SSHPublicKey = types.StringValue(cfg.SshPublicKey)
	} else {
		data.SSHPublicKey = types.StringNull()
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *BackupConfigResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var data BackupConfigResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, "Updating SSPI Backup Configuration")

	r.saveBackupConfig(ctx, &data, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	data.ID = types.StringValue("singleton")
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *BackupConfigResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data BackupConfigResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, "Deleting SSPI Backup Configuration")
}

func (r *BackupConfigResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
