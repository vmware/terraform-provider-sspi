// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.

package resource_backup

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/hashicorp/terraform-plugin-framework-validators/stringvalidator"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/schema/validator"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/vmware/terraform-provider-sspi/internal/client/api_client"
	"github.com/vmware/terraform-provider-sspi/internal/provider/backuprestorelcm"
)

// backupPollInterval and backupPollTimeout are vars, not consts, so unit
// tests in this package can temporarily shrink them (save/restore) to
// exercise the multi-iteration polling loop and deadline logic in
// waitForBackupComplete in milliseconds instead of the real 15s/90min
// production values.
var (
	backupPollInterval = 15 * time.Second
	backupPollTimeout  = 90 * time.Minute
)

var (
	_ resource.Resource              = &BackupResource{}
	_ resource.ResourceWithConfigure = &BackupResource{}
)

func NewBackupResource() resource.Resource {
	return &BackupResource{}
}

// BackupResource triggers an on-demand backup of the SSPI appliance.
type BackupResource struct {
	client *api_client.ClientWithResponses
}

// BackupResourceModel is the Terraform state model for on-demand backup.
type BackupResourceModel struct {
	ID          types.String `tfsdk:"id"`
	BackupType  types.String `tfsdk:"backup_type"`
	Action      types.String `tfsdk:"action"`
	Name        types.String `tfsdk:"name"`
	Description types.String `tfsdk:"description"`
	Status      types.String `tfsdk:"status"`
	Progress    types.Int64  `tfsdk:"progress"`
}

func (r *BackupResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_backup"
}

func (r *BackupResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Triggers an on-demand backup of the SSPI appliance.\n\n" +
			"Backup records are immutable audit trails: this resource has no `Update` " +
			"(any attribute change forces replacement) and no remote `Delete` — there is " +
			"no `DELETE /sspi/backup/status/{id}` API, so destroying this resource only " +
			"removes it from Terraform state.\n\n" +
			"Corresponds to `POST /sspi/backup` and `GET /sspi/backup/status/{id}`.",

		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Unique ID of the backup execution job.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"backup_type": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Type of backup to take. The SSPI API currently supports only `FULL_BACKUP`.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators: []validator.String{
					stringvalidator.OneOf("FULL_BACKUP"),
				},
			},
			"action": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Backup action type. The SSPI API currently supports only `BACKUP`. Defaults to `BACKUP`.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
				Validators: []validator.String{
					stringvalidator.OneOf("BACKUP"),
				},
			},
			"name": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Optional descriptive name for the backup.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"description": schema.StringAttribute{
				Optional:            true,
				MarkdownDescription: "Optional description for the backup.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"status": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Status of the backup execution (`SUCCESS`, `FAILED`, `IN_PROGRESS`, `QUEUED`, `NOT_STARTED`, `COMPLETED_WITH_ERRORS`).",
			},
			"progress": schema.Int64Attribute{
				Computed:            true,
				MarkdownDescription: "Percentage progress of the backup operation.",
			},
		},
	}
}

func (r *BackupResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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
				"(\"host\", \"username\", and \"password\", or the SSPI_HOST, SSPI_USERNAME, "+
				"and SSPI_PASSWORD environment variables).",
		)
		return
	}
}

func (r *BackupResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data BackupResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	action := api_client.BACKUP
	if !data.Action.IsNull() && data.Action.ValueString() != "" {
		action = api_client.BackupAction(data.Action.ValueString())
	}

	body := api_client.BackupRequest{
		BackupType: api_client.BackupType(data.BackupType.ValueString()),
		Action:     action,
	}
	if !data.Name.IsNull() {
		name := data.Name.ValueString()
		body.Name = &name
	}
	if !data.Description.IsNull() {
		desc := data.Description.ValueString()
		body.Description = &desc
	}

	// Backup competes with restore for the SSPI appliance's single backup/
	// restore subsystem; serialize with it via the shared mutex both
	// resources lock (see backuprestorelcm's doc comment).
	backuprestorelcm.Mu.Lock()
	defer backuprestorelcm.Mu.Unlock()

	startResp, err := r.client.StartBackupWithResponse(ctx, body)
	if err != nil {
		resp.Diagnostics.AddError("Error initiating backup", err.Error())
		return
	}
	if startResp.JSON202 == nil || startResp.JSON202.Id == nil || *startResp.JSON202.Id == "" {
		resp.Diagnostics.AddError(
			"Error initiating backup",
			fmt.Sprintf("Unexpected response from API: %d: %s", startResp.StatusCode(), string(startResp.Body)),
		)
		return
	}

	backupID := *startResp.JSON202.Id
	data.ID = types.StringValue(backupID)
	data.Action = types.StringValue(string(action))

	status, waitErr := r.waitForBackupComplete(ctx, backupID)
	if status != nil {
		mapBackupStatusToState(status, &data)
	} else {
		// The API already accepted and started this job (backupID is real)
		// even though the poll below didn't observe a terminal status.
		data.Status = types.StringValue(string(api_client.BackupRestoreStatusINPROGRESS))
		data.Progress = types.Int64Value(0)
	}

	// Persist state now, regardless of the poll outcome: the backup job was
	// genuinely created server-side, so losing track of its ID here would
	// cause the next apply to POST a second, duplicate/orphaned backup job.
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
	if waitErr != nil {
		resp.Diagnostics.AddError("Backup operation failed", waitErr.Error())
	}
}

func (r *BackupResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data BackupResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	statusResp, err := r.client.GetBackupStatusWithResponse(ctx, data.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Error reading backup status", err.Error())
		return
	}
	if statusResp.StatusCode() == http.StatusNotFound {
		resp.State.RemoveResource(ctx)
		return
	}
	if statusResp.JSON200 == nil {
		resp.Diagnostics.AddError(
			"Error reading backup status",
			fmt.Sprintf("Unexpected response from API: %d: %s", statusResp.StatusCode(), string(statusResp.Body)),
		)
		return
	}

	mapBackupStatusToState(statusResp.JSON200, &data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *BackupResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError("Update not supported", "Backup resources are immutable and cannot be updated.")
}

// Delete removes the resource from Terraform state only: there is no
// DELETE /sspi/backup/status/{id} API to remove a backup job record.
func (r *BackupResource) Delete(_ context.Context, _ resource.DeleteRequest, _ *resource.DeleteResponse) {
}

// waitForBackupComplete polls GET /sspi/backup/status/{id} until the backup
// reaches a terminal state, or backupPollTimeout elapses.
func (r *BackupResource) waitForBackupComplete(ctx context.Context, backupID string) (*api_client.BackupStatus, error) {
	deadline := time.Now().Add(backupPollTimeout)
	var lastErr error
	var lastStatus *api_client.BackupStatus

	for {
		if time.Now().After(deadline) {
			if lastErr != nil {
				return lastStatus, fmt.Errorf("timed out waiting for backup %s to complete (last error: %w)", backupID, lastErr)
			}
			return lastStatus, fmt.Errorf("timed out waiting for backup %s to complete", backupID)
		}

		statusResp, err := r.client.GetBackupStatusWithResponse(ctx, backupID)
		if err != nil {
			// Transient errors shouldn't abort a long-running backup wait;
			// keep polling until the deadline.
			lastErr = fmt.Errorf("error polling backup status: %w", err)
		} else if statusResp.JSON200 == nil {
			lastErr = fmt.Errorf("unexpected response polling backup status: %d: %s", statusResp.StatusCode(), string(statusResp.Body))
		} else {
			lastErr = nil
			lastStatus = statusResp.JSON200
			switch statusResp.JSON200.Status {
			case api_client.BackupRestoreStatusSUCCESS:
				return lastStatus, nil
			case api_client.BackupRestoreStatusFAILED, api_client.BackupRestoreStatusCOMPLETEDWITHERRORS:
				return lastStatus, fmt.Errorf("backup failed with status: %s", statusResp.JSON200.Status)
			}
		}

		select {
		case <-ctx.Done():
			return lastStatus, ctx.Err()
		case <-time.After(backupPollInterval):
		}
	}
}

func mapBackupStatusToState(s *api_client.BackupStatus, m *BackupResourceModel) {
	m.ID = types.StringValue(s.Id)
	m.Status = types.StringValue(string(s.Status))
	m.BackupType = types.StringValue(string(s.BackupType))
	if s.Progress != nil {
		m.Progress = types.Int64Value(int64(*s.Progress))
	} else {
		m.Progress = types.Int64Value(0)
	}
}
