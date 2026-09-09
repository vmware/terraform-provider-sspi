// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.

package resource_restore

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/vmware/terraform-provider-sspi/internal/client/api_client"
	"github.com/vmware/terraform-provider-sspi/internal/provider/backuprestorelcm"
)

// restorePollInterval and restorePollTimeout are vars, not consts, so unit
// tests in this package can temporarily shrink them (save/restore) to
// exercise the multi-iteration polling loop and deadline logic in
// waitForRestoreComplete in milliseconds instead of the real 15s/90min
// production values.
var (
	restorePollInterval = 15 * time.Second
	restorePollTimeout  = 90 * time.Minute
)

var (
	_ resource.Resource              = &RestoreResource{}
	_ resource.ResourceWithConfigure = &RestoreResource{}
)

func NewRestoreResource() resource.Resource {
	return &RestoreResource{}
}

// RestoreResource restores the SSPI appliance from a previously created backup.
type RestoreResource struct {
	client *api_client.ClientWithResponses
}

// RestoreResourceModel is the Terraform state model for restore execution.
type RestoreResourceModel struct {
	ID           types.String `tfsdk:"id"`
	BackupID     types.String `tfsdk:"backup_id"`
	ForceRestore types.Bool   `tfsdk:"force_restore"`
	Action       types.String `tfsdk:"action"`
	Status       types.String `tfsdk:"status"`
	Progress     types.Int64  `tfsdk:"progress"`
}

func (r *RestoreResource) Metadata(_ context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_restore"
}

func (r *RestoreResource) Schema(_ context.Context, _ resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Restores the SSPI appliance from a previously created backup.\n\n" +
			"Restore records are immutable audit trails: this resource has no `Update` " +
			"(any attribute change forces replacement) and no remote `Delete` — there is " +
			"no `DELETE /sspi/restore/status/{id}` API, so destroying this resource only " +
			"removes it from Terraform state.\n\n" +
			"Corresponds to `POST /sspi/restore` and `GET /sspi/restore/status/{id}`.",

		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Unique ID of the restore execution job.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"backup_id": schema.StringAttribute{
				Required:            true,
				MarkdownDescription: "Unique ID of the successful backup job to restore from.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"force_restore": schema.BoolAttribute{
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(false),
				MarkdownDescription: "When true, bypasses non-fatal validation warnings during restore.",
			},
			"action": schema.StringAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Restore action trigger. The SSPI API currently supports only `RESTORE`. Defaults to `RESTORE`.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.RequiresReplace()},
			},
			"status": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Status of the restore execution (`SUCCESS`, `FAILED`, `IN_PROGRESS`, `QUEUED`, `NOT_STARTED`, `COMPLETED_WITH_ERRORS`).",
			},
			"progress": schema.Int64Attribute{
				Computed:            true,
				MarkdownDescription: "Percentage progress of the restore operation.",
			},
		},
	}
}

func (r *RestoreResource) Configure(_ context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *RestoreResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data RestoreResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	action := api_client.RESTORE
	if !data.Action.IsNull() && data.Action.ValueString() != "" {
		action = api_client.RestoreAction(data.Action.ValueString())
	}

	body := api_client.RestoreRequest{
		BackupId: data.BackupID.ValueString(),
		Action:   action,
	}
	if !data.ForceRestore.IsNull() {
		force := data.ForceRestore.ValueBool()
		body.ForceRestore = &force
	}

	// Restore competes with backup for the SSPI appliance's single backup/
	// restore subsystem; serialize with it via the shared mutex both
	// resources lock (see backuprestorelcm's doc comment).
	backuprestorelcm.Mu.Lock()
	defer backuprestorelcm.Mu.Unlock()

	startResp, err := r.client.StartRestoreWithResponse(ctx, body)
	if err != nil {
		resp.Diagnostics.AddError("Error initiating restore", err.Error())
		return
	}
	if startResp.JSON202 == nil || startResp.JSON202.Id == nil || *startResp.JSON202.Id == "" {
		resp.Diagnostics.AddError(
			"Error initiating restore",
			fmt.Sprintf("Unexpected response from API: %d: %s", startResp.StatusCode(), string(startResp.Body)),
		)
		return
	}

	restoreID := *startResp.JSON202.Id
	data.ID = types.StringValue(restoreID)
	data.Action = types.StringValue(string(action))

	status, waitErr := r.waitForRestoreComplete(ctx, restoreID)
	if status != nil {
		mapRestoreStatusToState(status, &data)
	} else {
		// The API already accepted and started this job (restoreID is real)
		// even though the poll below didn't observe a terminal status.
		data.Status = types.StringValue(string(api_client.BackupRestoreStatusINPROGRESS))
		data.Progress = types.Int64Value(0)
	}

	// Persist state now, regardless of the poll outcome: the restore job was
	// genuinely created server-side, so losing track of its ID here would
	// cause the next apply to POST a second, duplicate/orphaned restore job.
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
	if waitErr != nil {
		resp.Diagnostics.AddError("Restore operation failed", waitErr.Error())
	}
}

func (r *RestoreResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data RestoreResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	statusResp, err := r.client.GetRestoreStatusWithResponse(ctx, data.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError("Error reading restore status", err.Error())
		return
	}
	if statusResp.StatusCode() == http.StatusNotFound {
		resp.State.RemoveResource(ctx)
		return
	}
	if statusResp.JSON200 == nil {
		resp.Diagnostics.AddError(
			"Error reading restore status",
			fmt.Sprintf("Unexpected response from API: %d: %s", statusResp.StatusCode(), string(statusResp.Body)),
		)
		return
	}

	mapRestoreStatusToState(statusResp.JSON200, &data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *RestoreResource) Update(_ context.Context, _ resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError("Update not supported", "Restore resources are immutable and cannot be updated.")
}

// Delete removes the resource from Terraform state only: there is no
// DELETE /sspi/restore/status/{id} API to remove a restore job record.
func (r *RestoreResource) Delete(_ context.Context, _ resource.DeleteRequest, _ *resource.DeleteResponse) {
}

// waitForRestoreComplete polls GET /sspi/restore/status/{id} until the
// restore reaches a terminal state, or restorePollTimeout elapses.
func (r *RestoreResource) waitForRestoreComplete(ctx context.Context, restoreID string) (*api_client.RestoreStatus, error) {
	deadline := time.Now().Add(restorePollTimeout)
	var lastErr error
	var lastStatus *api_client.RestoreStatus

	for {
		if time.Now().After(deadline) {
			if lastErr != nil {
				return lastStatus, fmt.Errorf("timed out waiting for restore %s to complete (last error: %w)", restoreID, lastErr)
			}
			return lastStatus, fmt.Errorf("timed out waiting for restore %s to complete", restoreID)
		}

		statusResp, err := r.client.GetRestoreStatusWithResponse(ctx, restoreID)
		if err != nil {
			// Transient errors shouldn't abort a long-running restore wait;
			// keep polling until the deadline.
			lastErr = fmt.Errorf("error polling restore status: %w", err)
		} else if statusResp.JSON200 == nil {
			lastErr = fmt.Errorf("unexpected response polling restore status: %d: %s", statusResp.StatusCode(), string(statusResp.Body))
		} else {
			lastErr = nil
			lastStatus = statusResp.JSON200
			switch statusResp.JSON200.Status {
			case api_client.BackupRestoreStatusSUCCESS:
				return lastStatus, nil
			case api_client.BackupRestoreStatusFAILED, api_client.BackupRestoreStatusCOMPLETEDWITHERRORS:
				return lastStatus, fmt.Errorf("restore failed with status: %s", statusResp.JSON200.Status)
			}
		}

		select {
		case <-ctx.Done():
			return lastStatus, ctx.Err()
		case <-time.After(restorePollInterval):
		}
	}
}

func mapRestoreStatusToState(s *api_client.RestoreStatus, m *RestoreResourceModel) {
	m.ID = types.StringValue(s.Id)
	m.Status = types.StringValue(string(s.Status))
	if s.BackupId != nil {
		m.BackupID = types.StringValue(*s.BackupId)
	}
	if s.Progress != nil {
		m.Progress = types.Int64Value(int64(*s.Progress))
	} else {
		m.Progress = types.Int64Value(0)
	}
}
