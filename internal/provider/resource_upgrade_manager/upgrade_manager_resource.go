// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.

package resource_upgrade_manager

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"github.com/vmware/terraform-provider-sspi/internal/client/upgrade_client"
)

// upgradeManagerMu serialises Upgrade Manager upgrades within one apply: the
// appliance allows only one Upgrade Manager upgrade at a time.
var upgradeManagerMu sync.Mutex

const upgradeManagerSingletonID = "upgrade-manager"

// Vars (not consts) so unit tests can shrink them.
var (
	pollInterval = 15 * time.Second
	pollTimeout  = 90 * time.Minute
)

var (
	_ resource.Resource                = &UpgradeManagerResource{}
	_ resource.ResourceWithConfigure   = &UpgradeManagerResource{}
	_ resource.ResourceWithImportState = &UpgradeManagerResource{}
)

func NewUpgradeManagerResource() resource.Resource {
	return &UpgradeManagerResource{}
}

type UpgradeManagerResource struct {
	client *upgrade_client.ClientWithResponses
}

type UpgradeManagerResourceModel struct {
	ID            types.String `tfsdk:"id"`
	TargetVersion types.String `tfsdk:"target_version"`
	Status        types.String `tfsdk:"status"`
	Progress      types.Int64  `tfsdk:"progress"`
	CurrentStep   types.String `tfsdk:"current_step"`
}

func (r *UpgradeManagerResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_upgrade_manager"
}

func (r *UpgradeManagerResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Upgrades the SSPI **Upgrade Manager** service (the orchestration tool that performs " +
			"appliance upgrades) to the version in a staged upgrade package.\n\n" +
			"This must complete before `sspi_upgrade` can run: until the Upgrade Manager is at the target " +
			"version, `POST /sspi/upgrade` is rejected with HTTP 400 (\"please upgrade the upgrade manager " +
			"before running the pre-checks\"). Express the ordering with `depends_on = [sspi_upgrade_manager.<name>]` " +
			"on `sspi_upgrade`, and stage the package first with `sspi_installer_bundle_remote` or " +
			"`sspi_installer_bundle_local`.\n\n" +
			"On create, if the Upgrade Manager upgrade has already completed successfully the resource adopts " +
			"that state without re-running it; otherwise it issues `POST /sspi/upgrade/manager?action=START` " +
			"and polls `GET /sspi/upgrade/manager/status` until it reaches a terminal state. If a previous " +
			"attempt FAILED, the same call retries it.\n\n" +
			"There is no delete API, so `Delete` only removes the resource from Terraform state. The Upgrade " +
			"Manager is a singleton per appliance.\n\n" +
			"Corresponds to `POST /sspi/upgrade/manager` and `GET /sspi/upgrade/manager/status`.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Fixed identifier (`upgrade-manager`).",
				PlanModifiers:       []planmodifier.String{stringplanmodifier.UseStateForUnknown()},
			},
			"target_version": schema.StringAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Version to upgrade the Upgrade Manager to; must match a version returned by " +
					"`GET /sspi/upgrade/packages` (see the `sspi_upgrade_packages` data source). If omitted, the " +
					"version of the single staged upgrade package is used; it is an error if zero or more than one " +
					"package is staged. Changing it forces replacement.",
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
					stringplanmodifier.RequiresReplaceIfConfigured(),
				},
			},
			"status": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Overall status of the Upgrade Manager upgrade (`NOT_STARTED`, `IN_PROGRESS`, `SUCCESS`, `SUCCESS_WITH_WARNINGS`, `FAILED`, `PAUSED`).",
			},
			"progress": schema.Int64Attribute{
				Computed:            true,
				MarkdownDescription: "Progress of the Upgrade Manager upgrade as a percentage (0-100).",
			},
			"current_step": schema.StringAttribute{
				Computed:            true,
				MarkdownDescription: "Display name of the step currently executing (or the last step executed).",
			},
		},
	}
}

func (r *UpgradeManagerResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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
	}
}

func (r *UpgradeManagerResource) currentStatus(ctx context.Context) (*upgrade_client.UpgradeManagerStatus, error) {
	resp, err := r.client.GetUpgradeManagerStatusWithResponse(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading upgrade manager status: %w", err)
	}
	if resp.JSON200 == nil {
		return nil, fmt.Errorf("unexpected response reading upgrade manager status: %d: %s", resp.StatusCode(), string(resp.Body))
	}
	return resp.JSON200, nil
}

// resolveTargetVersion returns the configured version, or the version of the
// single staged upgrade package.
func (r *UpgradeManagerResource) resolveTargetVersion(ctx context.Context, configured types.String) (string, error) {
	if !configured.IsNull() && !configured.IsUnknown() && configured.ValueString() != "" {
		return configured.ValueString(), nil
	}
	resp, err := r.client.GetUpgradePackagesWithResponse(ctx)
	if err != nil {
		return "", fmt.Errorf("listing upgrade packages: %w", err)
	}
	if resp.JSON200 == nil {
		return "", fmt.Errorf("unexpected response listing upgrade packages: %d: %s", resp.StatusCode(), string(resp.Body))
	}
	switch n := len(resp.JSON200.Results); n {
	case 0:
		return "", fmt.Errorf("no upgrade package is staged on the appliance; import one with sspi_installer_bundle_remote/local first")
	case 1:
		return resp.JSON200.Results[0].Version, nil
	default:
		return "", fmt.Errorf("%d upgrade packages are staged; set target_version explicitly", n)
	}
}

func (r *UpgradeManagerResource) waitForTerminal(ctx context.Context) (*upgrade_client.UpgradeManagerStatus, error) {
	deadline := time.Now().Add(pollTimeout)
	var lastErr error
	var last *upgrade_client.UpgradeManagerStatus

	for {
		status, err := r.currentStatus(ctx)
		if err != nil {
			// Transient errors (e.g. the manager restarting itself) shouldn't abort the wait.
			lastErr = err
		} else {
			lastErr = nil
			last = status
			if isTerminal(status.OverallStatus) {
				return status, nil
			}
		}

		if time.Now().After(deadline) {
			if lastErr != nil {
				return nil, fmt.Errorf("timed out waiting for upgrade manager upgrade (last error: %w)", lastErr)
			}
			return last, fmt.Errorf("timed out waiting for upgrade manager upgrade, last overall_status: %s", statusString(last))
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(pollInterval):
		}
	}
}

func (r *UpgradeManagerResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data UpgradeManagerResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	upgradeManagerMu.Lock()
	defer upgradeManagerMu.Unlock()

	status, version, err := r.upgrade(ctx, data.TargetVersion)
	data.ID = types.StringValue(upgradeManagerSingletonID)
	data.TargetVersion = types.StringValue(version)
	if status != nil {
		mapStatusToState(status, &data)
	} else {
		// Computed attributes must not stay Unknown after apply.
		data.Status = types.StringNull()
		data.Progress = types.Int64Null()
		data.CurrentStep = types.StringNull()
	}
	if err != nil && version == "" {
		data.TargetVersion = types.StringNull()
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
	if err != nil {
		resp.Diagnostics.AddError("Upgrade Manager upgrade failed", err.Error())
	}
}

// upgrade drives the Upgrade Manager to SUCCESS, returning the resolved target version.
func (r *UpgradeManagerResource) upgrade(ctx context.Context, configured types.String) (*upgrade_client.UpgradeManagerStatus, string, error) {
	version, err := r.resolveTargetVersion(ctx, configured)
	if err != nil {
		return nil, "", err
	}

	current, err := r.currentStatus(ctx)
	if err != nil {
		return nil, version, err
	}
	if isSuccess(current.OverallStatus) {
		return current, version, nil
	}

	postResp, err := r.client.TriggerUpgradeManagerUpgradeWithResponse(ctx,
		&upgrade_client.TriggerUpgradeManagerUpgradeParams{Action: upgrade_client.TriggerUpgradeManagerUpgradeParamsActionSTART},
		upgrade_client.UpgradeManagerRequest{TargetVersion: version})
	if err != nil {
		return nil, version, fmt.Errorf("triggering upgrade manager upgrade: %w", err)
	}
	// 409 with an in-progress run is fine: fall through and wait for it.
	if postResp.JSON202 == nil && postResp.StatusCode() != http.StatusConflict {
		return nil, version, fmt.Errorf("unexpected response triggering upgrade manager upgrade: %d: %s", postResp.StatusCode(), string(postResp.Body))
	}

	status, err := r.waitForTerminal(ctx)
	if err != nil {
		return status, version, err
	}
	if !isSuccess(status.OverallStatus) {
		return status, version, fmt.Errorf("upgrade manager upgrade did not complete successfully, overall_status: %s", statusString(status))
	}
	return status, version, nil
}

func (r *UpgradeManagerResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data UpgradeManagerResourceModel
	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	sdkResp, err := r.client.GetUpgradeManagerStatusWithResponse(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Error reading upgrade manager status", err.Error())
		return
	}
	if sdkResp.StatusCode() == http.StatusNotFound {
		resp.State.RemoveResource(ctx)
		return
	}
	if sdkResp.JSON200 == nil {
		resp.Diagnostics.AddError(
			"Error reading upgrade manager status",
			fmt.Sprintf("Unexpected response from API: %d: %s", sdkResp.StatusCode(), string(sdkResp.Body)),
		)
		return
	}

	mapStatusToState(sdkResp.JSON200, &data)
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// Update re-runs a FAILED upgrade; otherwise it only refreshes state.
func (r *UpgradeManagerResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var data UpgradeManagerResourceModel
	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	current, err := r.currentStatus(ctx)
	if err != nil {
		resp.Diagnostics.AddError("Error reading upgrade manager status", err.Error())
		return
	}
	if statusString(current) != string(upgrade_client.FAILED) {
		mapStatusToState(current, &data)
		resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
		return
	}

	upgradeManagerMu.Lock()
	defer upgradeManagerMu.Unlock()

	status, version, err := r.upgrade(ctx, data.TargetVersion)
	data.ID = types.StringValue(upgradeManagerSingletonID)
	if version != "" {
		data.TargetVersion = types.StringValue(version)
	}
	if status != nil {
		mapStatusToState(status, &data)
	}
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
	if err != nil {
		resp.Diagnostics.AddError("Upgrade Manager retry failed", err.Error())
	}
}

// Delete only drops the resource from state: there is no API to undo or clear it.
func (r *UpgradeManagerResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
}

func (r *UpgradeManagerResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resp.Diagnostics.Append(resp.State.SetAttribute(ctx, path.Root("id"), upgradeManagerSingletonID)...)
}

func isTerminal(s *upgrade_client.BaseUpgradeStatus) bool {
	switch deref(s) {
	case string(upgrade_client.SUCCESS), string(upgrade_client.SUCCESSWITHWARNINGS),
		string(upgrade_client.FAILED), string(upgrade_client.PAUSED):
		return true
	default:
		return false
	}
}

func isSuccess(s *upgrade_client.BaseUpgradeStatus) bool {
	switch deref(s) {
	case string(upgrade_client.SUCCESS), string(upgrade_client.SUCCESSWITHWARNINGS):
		return true
	default:
		return false
	}
}

func deref(s *upgrade_client.BaseUpgradeStatus) string {
	if s == nil {
		return ""
	}
	return string(*s)
}

func statusString(s *upgrade_client.UpgradeManagerStatus) string {
	if s == nil {
		return ""
	}
	return deref(s.OverallStatus)
}

func currentStepName(s *upgrade_client.UpgradeManagerStatus) string {
	if s.CurrentStep != nil && *s.CurrentStep != "" {
		return *s.CurrentStep
	}
	for _, step := range s.UpgradeSteps {
		if deref(step.Status) == string(upgrade_client.INPROGRESS) {
			return step.DisplayName
		}
	}
	if n := len(s.UpgradeSteps); n > 0 {
		return s.UpgradeSteps[n-1].DisplayName
	}
	return ""
}

func mapStatusToState(s *upgrade_client.UpgradeManagerStatus, m *UpgradeManagerResourceModel) {
	m.ID = types.StringValue(upgradeManagerSingletonID)
	m.Status = types.StringValue(statusString(s))
	m.Progress = types.Int64Value(int64(s.ProgressPercentage))
	m.CurrentStep = types.StringValue(currentStepName(s))
}
