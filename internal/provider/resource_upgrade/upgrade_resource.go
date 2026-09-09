// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.

package resource_upgrade

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/booldefault"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/vmware/terraform-provider-sspi/internal/client/upgrade_client"
)

// upgradeLcmMu serialises SSPI appliance self-upgrade actions (trigger /
// retry) within a single Terraform apply: the SSPI appliance only supports
// one concurrent upgrade workflow. This is deliberately a separate mutex
// from platformLcmMu (internal/provider/resource_platform) — appliance
// self-upgrade and platform/cluster LCM are distinct backend workflow slots,
// so serialising them together would only add unnecessary contention with
// no correctness benefit.
var upgradeLcmMu sync.Mutex

// upgradeSingletonID is the fixed Terraform ID for sspi_upgrade: the
// SSPI public API exposes a single, appliance-wide upgrade workflow
// with no per-request identifier, so the resource is modeled as a
// singleton against GET /sspi/upgrade/status.
const upgradeSingletonID = "upgrade"

// upgradePollInterval and upgradePollTimeout are vars, not consts, so unit
// tests in this package can temporarily shrink them (save/restore) to
// exercise the multi-iteration polling loop and deadline logic in
// waitForOverallStatus/waitForPrechecksComplete in milliseconds instead of
// the real 15s/90min production values.
var (
	upgradePollInterval = 15 * time.Second
	upgradePollTimeout  = 90 * time.Minute
)

// Ensure the implementation satisfies the expected interfaces.
var (
	_ resource.Resource                = &UpgradeResource{}
	_ resource.ResourceWithConfigure   = &UpgradeResource{}
	_ resource.ResourceWithImportState = &UpgradeResource{}
)

func NewUpgradeResource() resource.Resource {
	return &UpgradeResource{}
}

// UpgradeResource manages SSPI appliance self-upgrade execution.
type UpgradeResource struct {
	client *upgrade_client.ClientWithResponses
}

// UpgradeResourceModel is the Terraform state model for appliance upgrade execution.
type UpgradeResourceModel struct {
	ID             types.String `tfsdk:"id"`
	RunPrechecks   types.Bool   `tfsdk:"run_prechecks"`
	CurrentVersion types.String `tfsdk:"current_version"`
	TargetVersion  types.String `tfsdk:"target_version"`
	Status         types.String `tfsdk:"status"`
	Progress       types.Int64  `tfsdk:"progress"`
	CurrentStep    types.String `tfsdk:"current_step"`
}

func (r *UpgradeResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_upgrade"
}

func (r *UpgradeResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Triggers and tracks an SSPI appliance self-upgrade.\n\n" +
			"The SSPI upgrade workflow has no user-selectable target version and no request\n" +
			"body: `POST /sspi/upgrade` is driven entirely by the `action` query parameter\n" +
			"(`PRECHECKS_ONLY`, `START`, `CONTINUE`, `RETRY`), and `target_version` /\n" +
			"`current_version` are read-only values reported by `GET /sspi/upgrade/status`\n" +
			"once an upgrade package has been staged.\n\n" +
			"On create:\n" +
			"  - if the last upgrade attempt is `FAILED`, issues `action=RETRY`;\n" +
			"  - else if `run_prechecks` is `true` (default), issues `action=PRECHECKS_ONLY`\n" +
			"    first and only proceeds to `action=CONTINUE` if pre-checks succeed;\n" +
			"  - else issues `action=START` directly.\n\n" +
			"There is no `DELETE /sspi/upgrade` API, so `Delete` only removes this resource\n" +
			"from Terraform state — the appliance's upgrade history is left in place (see the\n" +
			"FSDD's singleton-resource-destroy-semantics note). There is only one upgrade\n" +
			"workflow per SSPI appliance, so this resource is a singleton.\n\n" +
			"**Critical sequencing requirement:** the SSPI Installer must be upgraded before\n" +
			"uploading a new SSP/Avi Operations package and before upgrading the SSP cluster\n" +
			"itself (`ssp_upgrade` in the companion `terraform-provider-ssp`). Express this\n" +
			"ordering with `depends_on = [sspi_upgrade.<name>]` on the `ssp_upgrade` resource.\n\n" +
			"Corresponds to `POST /sspi/upgrade` and `GET /sspi/upgrade/status`.",

		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Fixed identifier (`upgrade`); the SSPI API exposes a single appliance-wide upgrade workflow.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"run_prechecks": schema.BoolAttribute{
				Optional:            true,
				Computed:            true,
				Default:             booldefault.StaticBool(true),
				MarkdownDescription: "Whether to run validation pre-checks before starting the upgrade. Ignored if a previous upgrade attempt failed (in which case the failed attempt is retried via `action=RETRY`).",
			},
			"current_version": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "SSPI appliance version immediately before the upgrade, as reported by the API.",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"target_version": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "SSPI appliance version being upgraded to, as reported by the API. Not user-settable — `POST /sspi/upgrade` has no version parameter.",
			},
			"status": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Overall status of the upgrade operation (`NOT_STARTED`, `IN_PROGRESS`, `SUCCESS`, `SUCCESS_WITH_WARNINGS`, `FAILED`, `PAUSED`).",
			},
			"progress": schema.Int64Attribute{
				Computed:            true,
				MarkdownDescription: "Percentage of upgrade steps that have completed successfully (0-100).",
			},
			"current_step": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Display name of the upgrade step currently executing (or the last step executed).",
			},
		},
	}
}

func (r *UpgradeResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	clientProvider, ok := req.ProviderData.(interface {
		GetUpgrade() *upgrade_client.ClientWithResponses
	})

	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Resource Configure Type",
			fmt.Sprintf("Expected provider data with GetUpgrade(), got: %T. Please report this issue to the provider developers.", req.ProviderData),
		)
		return
	}

	r.client = clientProvider.GetUpgrade()
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

// currentUpgradeStatus fetches GET /sspi/upgrade/status.
func (r *UpgradeResource) currentUpgradeStatus(ctx context.Context) (*upgrade_client.SspiUpgradeStatus, error) {
	resp, err := r.client.GetSspiUpgradeStatusWithResponse(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading upgrade status: %w", err)
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("unexpected response reading upgrade status: %d: %s", resp.StatusCode(), string(resp.Body))
	}
	return resp.JSON200, nil
}

// waitForOverallStatus polls GET /sspi/upgrade/status until overall_status
// reaches a terminal state, mirroring ssp_upgrade's WaitForUpgradeStatus.
func (r *UpgradeResource) waitForOverallStatus(ctx context.Context) (*upgrade_client.SspiUpgradeStatus, error) {
	deadline := time.Now().Add(upgradePollTimeout)
	var lastErr error
	var lastStatus *upgrade_client.SspiUpgradeStatus

	for {
		status, err := r.currentUpgradeStatus(ctx)
		if err != nil {
			// Transient errors shouldn't abort a long-running upgrade wait;
			// keep polling until the deadline.
			lastErr = err
		} else {
			lastErr = nil
			lastStatus = status
			if isTerminalOverallStatus(status.OverallStatus) {
				return status, nil
			}
		}

		if time.Now().After(deadline) {
			if lastErr != nil {
				return nil, fmt.Errorf("timed out waiting for upgrade status to settle (last error: %w)", lastErr)
			}
			return lastStatus, fmt.Errorf("timed out waiting for upgrade status to settle, last overall_status: %s", overallStatusString(lastStatus))
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(upgradePollInterval):
		}
	}
}

// waitForUpgradeComplete polls until the upgrade reaches a terminal state,
// returning an error if it did not complete successfully.
func (r *UpgradeResource) waitForUpgradeComplete(ctx context.Context) (*upgrade_client.SspiUpgradeStatus, error) {
	status, err := r.waitForOverallStatus(ctx)
	if err != nil {
		return status, err
	}
	switch derefStatus(status.OverallStatus) {
	case string(upgrade_client.SUCCESS), string(upgrade_client.SUCCESSWITHWARNINGS):
		return status, nil
	default:
		return status, fmt.Errorf("upgrade did not complete successfully, overall_status: %s", overallStatusString(status))
	}
}

// waitForPrechecksComplete polls GET /sspi/upgrade/status until
// pre_checks_status.overall_status reaches a terminal state.
func (r *UpgradeResource) waitForPrechecksComplete(ctx context.Context) (*upgrade_client.SspiUpgradeStatus, error) {
	deadline := time.Now().Add(upgradePollTimeout)
	var lastErr error
	var lastStatus *upgrade_client.SspiUpgradeStatus

	for {
		status, err := r.currentUpgradeStatus(ctx)
		if err != nil {
			lastErr = err
		} else {
			lastErr = nil
			lastStatus = status
			if status.PreChecksStatus != nil && isTerminalOverallStatus(status.PreChecksStatus.OverallStatus) {
				return status, nil
			}
		}

		if time.Now().After(deadline) {
			if lastErr != nil {
				return nil, fmt.Errorf("timed out waiting for upgrade pre-checks to settle (last error: %w)", lastErr)
			}
			return lastStatus, fmt.Errorf("timed out waiting for upgrade pre-checks to settle")
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(upgradePollInterval):
		}
	}
}

// triggerUpgrade determines the correct action (RETRY / PRECHECKS_ONLY+CONTINUE
// / START) based on the current upgrade status and run_prechecks, then drives
// the upgrade to completion (or a reported pre-checks/upgrade failure).
func (r *UpgradeResource) triggerUpgrade(ctx context.Context, runPrechecks bool) (*upgrade_client.SspiUpgradeStatus, error) {
	current, err := r.currentUpgradeStatus(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading current upgrade status: %w", err)
	}

	if derefStatus(current.OverallStatus) == string(upgrade_client.FAILED) {
		if err := r.postAction(ctx, upgrade_client.UpgradeActionRETRY); err != nil {
			return nil, fmt.Errorf("retrying failed upgrade: %w", err)
		}
		return r.waitForUpgradeComplete(ctx)
	}

	if runPrechecks {
		if err := r.postAction(ctx, upgrade_client.UpgradeActionPRECHECKSONLY); err != nil {
			return nil, fmt.Errorf("running upgrade pre-checks: %w", err)
		}
		precheckStatus, err := r.waitForPrechecksComplete(ctx)
		if err != nil {
			return nil, fmt.Errorf("waiting for pre-checks to complete: %w", err)
		}
		if precheckStatus.PreChecksStatus != nil &&
			derefStatus(precheckStatus.PreChecksStatus.OverallStatus) == string(upgrade_client.FAILED) {
			return precheckStatus, fmt.Errorf("upgrade pre-checks failed")
		}

		if err := r.postAction(ctx, upgrade_client.UpgradeActionCONTINUE); err != nil {
			return nil, fmt.Errorf("continuing upgrade after successful pre-checks: %w", err)
		}
		return r.waitForUpgradeComplete(ctx)
	}

	if err := r.postAction(ctx, upgrade_client.UpgradeActionSTART); err != nil {
		return nil, fmt.Errorf("starting upgrade: %w", err)
	}
	return r.waitForUpgradeComplete(ctx)
}

// postAction issues POST /sspi/upgrade?action=<action>.
func (r *UpgradeResource) postAction(ctx context.Context, action upgrade_client.UpgradeAction) error {
	resp, err := r.client.TriggerSspiUpgradeWithResponse(ctx, &upgrade_client.TriggerSspiUpgradeParams{Action: action})
	if err != nil {
		return err
	}
	if resp.JSON202 != nil {
		return nil
	}
	return fmt.Errorf("unexpected response triggering action %s: %d: %s", action, resp.StatusCode(), string(resp.Body))
}

func (r *UpgradeResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data UpgradeResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	upgradeLcmMu.Lock()
	defer upgradeLcmMu.Unlock()

	status, err := r.triggerUpgrade(ctx, data.RunPrechecks.ValueBool())
	if status != nil {
		mapUpgradeStatusToState(status, &data)
	} else {
		data.ID = types.StringValue(upgradeSingletonID)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
	if err != nil {
		resp.Diagnostics.AddError("Upgrade operation failed", err.Error())
	}
}

func (r *UpgradeResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), upgradeSingletonID)...)
}

func (r *UpgradeResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data UpgradeResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	sdkResp, err := r.client.GetSspiUpgradeStatusWithResponse(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Error reading upgrade status", err.Error())
		return
	}
	if sdkResp.StatusCode() == http.StatusNotFound {
		resp.State.RemoveResource(ctx)
		return
	}
	if sdkResp.JSON200 == nil {
		resp.Diagnostics.AddError(
			"Error reading upgrade status",
			fmt.Sprintf("Unexpected response from API: %d: %s", sdkResp.StatusCode(), string(sdkResp.Body)),
		)
		return
	}

	mapUpgradeStatusToState(sdkResp.JSON200, &data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// Update retries a FAILED upgrade (action=RETRY). Other attribute changes
// (e.g. run_prechecks) have no effect once an upgrade has been triggered.
func (r *UpgradeResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var data UpgradeResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	current, err := r.currentUpgradeStatus(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Error reading current upgrade status", err.Error())
		return
	}

	if derefStatus(current.OverallStatus) != string(upgrade_client.FAILED) {
		mapUpgradeStatusToState(current, &data)
		resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
		return
	}

	upgradeLcmMu.Lock()
	defer upgradeLcmMu.Unlock()

	if err := r.postAction(ctx, upgrade_client.UpgradeActionRETRY); err != nil {
		resp.Diagnostics.AddError("Error retrying failed upgrade", err.Error())
		return
	}

	status, err := r.waitForUpgradeComplete(ctx)
	if status != nil {
		mapUpgradeStatusToState(status, &data)
	} else {
		data.ID = types.StringValue(upgradeSingletonID)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
	if err != nil {
		resp.Diagnostics.AddError("Upgrade retry failed", err.Error())
	}
}

// Delete removes the resource from Terraform state only: there is no
// DELETE /sspi/upgrade API to undo an appliance upgrade or clear its
// history (see FSDD §5.1.2's singleton-resource-destroy-semantics note).
func (r *UpgradeResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
}

func isTerminalOverallStatus(s *upgrade_client.BaseUpgradeStatus) bool {
	switch derefStatus(s) {
	case string(upgrade_client.SUCCESS), string(upgrade_client.SUCCESSWITHWARNINGS),
		string(upgrade_client.FAILED), string(upgrade_client.PAUSED), string(upgrade_client.NOTSTARTED):
		return true
	default:
		return false
	}
}

func derefStatus(s *upgrade_client.BaseUpgradeStatus) string {
	if s == nil {
		return ""
	}
	return string(*s)
}

func overallStatusString(s *upgrade_client.SspiUpgradeStatus) string {
	if s == nil {
		return ""
	}
	return derefStatus(s.OverallStatus)
}

// currentStepName returns the display name of the first in-progress upgrade
// step, or the last step in the list if none is currently running.
func currentStepName(steps []upgrade_client.SspiUpgradeStepStatus) string {
	for _, step := range steps {
		if derefStatus(step.Status) == string(upgrade_client.INPROGRESS) {
			return step.DisplayName
		}
	}
	if n := len(steps); n > 0 {
		return steps[n-1].DisplayName
	}
	return ""
}

// stepProgressPercent returns the percentage of upgrade_steps in a terminal
// success state (SUCCESS, SUCCESS_WITH_WARNINGS, or SKIPPED).
func stepProgressPercent(steps []upgrade_client.SspiUpgradeStepStatus) int64 {
	if len(steps) == 0 {
		return 0
	}
	done := 0
	for _, step := range steps {
		switch derefStatus(step.Status) {
		case string(upgrade_client.SUCCESS), string(upgrade_client.SUCCESSWITHWARNINGS), string(upgrade_client.SKIPPED):
			done++
		}
	}
	return int64(done * 100 / len(steps))
}

func mapUpgradeStatusToState(s *upgrade_client.SspiUpgradeStatus, m *UpgradeResourceModel) {
	m.ID = types.StringValue(upgradeSingletonID)
	m.Status = types.StringValue(derefStatus(s.OverallStatus))
	if s.CurrentVersion != nil {
		m.CurrentVersion = types.StringValue(*s.CurrentVersion)
	} else {
		m.CurrentVersion = types.StringValue("")
	}
	if s.TargetVersion != nil {
		m.TargetVersion = types.StringValue(*s.TargetVersion)
	} else {
		m.TargetVersion = types.StringValue("")
	}
	m.Progress = types.Int64Value(stepProgressPercent(s.UpgradeSteps))
	m.CurrentStep = types.StringValue(currentStepName(s.UpgradeSteps))
}
