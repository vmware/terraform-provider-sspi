// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.

package provider

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
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

// platformLcmMu serialises SSPI platform lifecycle actions (create / update /
// teardown) within a single Terraform apply: the SSPI appliance only supports
// one concurrent precheck-then-LCM workflow per apply. This is deliberately a
// separate mutex from the SSP-cluster-side lcmMu (internal/provider package)
// rather than a shared one — ssp_platform operates against the SSPI installer
// appliance, a distinct backend from the deployed SSP cluster runtime that
// ssp_feature/ssp_site/ssp_upgrade/ssp_backup/ssp_restore and the LCM config
// resources target, so serializing them together would only add unnecessary
// contention with no correctness benefit.
var platformLcmMu sync.Mutex

// Ensure the implementation satisfies the expected interfaces.
var (
	_ resource.Resource                = &PlatformResource{}
	_ resource.ResourceWithConfigure   = &PlatformResource{}
	_ resource.ResourceWithImportState = &PlatformResource{}
)

// platformPollInterval and platformPollTimeout are vars, not consts, so unit
// tests in this package can temporarily shrink them (save/restore) to
// exercise the multi-iteration polling loop and deadline logic in
// waitForWorkflow/Delete in milliseconds instead of the real 15s/90min
// production values.
var (
	platformPollInterval = 15 * time.Second
	platformPollTimeout  = 90 * time.Minute
)

// NewPlatformResource is a helper function to simplify the provider implementation.
func NewPlatformResource() resource.Resource {
	return &PlatformResource{}
}

// PlatformResource is the resource implementation.
type PlatformResource struct {
	client *api_client.ClientWithResponses
}

// PlatformResourceModel describes the resource data model.
type PlatformResourceModel struct {
	ID          types.String `tfsdk:"id"`
	ProviderID  types.String `tfsdk:"provider_id"`
	DisplayName types.String `tfsdk:"display_name"`
	FormFactor  types.String `tfsdk:"form_factor"`
	SspType     types.String `tfsdk:"ssp_type"`

	WorkerCount     types.Int64 `tfsdk:"worker_count"`
	ControllerCount types.Int64 `tfsdk:"controller_count"`

	DatacenterID              types.String `tfsdk:"datacenter_id"`
	DatacenterName            types.String `tfsdk:"datacenter_name"`
	ClusterID                 types.String `tfsdk:"cluster_id"`
	ClusterName               types.String `tfsdk:"cluster_name"`
	ContentDatastoreID        types.String `tfsdk:"content_datastore_id"`
	Datastore                 types.String `tfsdk:"datastore"`
	StoragePolicyID           types.String `tfsdk:"storage_policy_id"`
	StoragePolicyName         types.String `tfsdk:"storage_policy_name"`
	VmFolderName              types.String `tfsdk:"vm_folder_name"`
	VmTemplate                types.String `tfsdk:"vm_template"`
	EnableResourceReservation types.Bool   `tfsdk:"enable_resource_reservation"`

	Domain     types.String `tfsdk:"domain"`
	DnsServers types.List   `tfsdk:"dns_servers"`
	NtpServer  types.String `tfsdk:"ntp_server"`

	NetworkID              types.String `tfsdk:"network_id"`
	Network                types.String `tfsdk:"network"`
	PortgroupID            types.String `tfsdk:"portgroup_id"`
	PortgroupName          types.String `tfsdk:"portgroup_name"`
	PlatformDefaultGateway types.String `tfsdk:"platform_default_gateway"`
	PlatformSubnet         types.String `tfsdk:"platform_subnet"`
	NodeIPPool             types.List   `tfsdk:"node_ip_pool"`
	ServiceIPPool          types.List   `tfsdk:"service_ip_pool"`

	IngressFqdn      types.String `tfsdk:"ingress_fqdn"`
	KafkaFqdn        types.String `tfsdk:"kafka_fqdn"`
	SspBundleID      types.String `tfsdk:"ssp_bundle_id"`
	SspBundleVersion types.String `tfsdk:"ssp_bundle_version"`
	PreserveAddons   types.Bool   `tfsdk:"preserve_addons"`
	AddonIDs         types.List   `tfsdk:"addon_ids"`
	AdminPassword    types.String `tfsdk:"admin_password"`
	AuditPassword    types.String `tfsdk:"audit_password"`

	Status types.String `tfsdk:"status"`
}

func (r *PlatformResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_platform"
}

func (r *PlatformResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages a Security Service Platform (SSP) or Avi Operations instance deployment.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier for the SSP platform instance.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"provider_id": schema.StringAttribute{
				MarkdownDescription: "The ID of the vSphere provider (`ssp_vsphere_provider`) to deploy the platform to.",
				Required:            true,
			},
			"display_name": schema.StringAttribute{
				MarkdownDescription: "The instance name for the platform. Not editable after creation.",
				Required:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"form_factor": schema.StringAttribute{
				MarkdownDescription: "The compute form factor of the platform. One of `COMPACT`, `MEDIUM`, `LARGE`, `EXTRA_LARGE`.",
				Required:            true,
			},
			"ssp_type": schema.StringAttribute{
				MarkdownDescription: "The type of SSP deployment. One of `ATP`, `LICENSING`, `AVI_OPERATIONS`. Defaults to `ATP`.",
				Optional:            true,
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"worker_count": schema.Int64Attribute{
				MarkdownDescription: "The number of worker nodes. Valid range depends on `form_factor`/`ssp_type`.",
				Optional:            true,
				Computed:            true,
			},
			"controller_count": schema.Int64Attribute{
				MarkdownDescription: "The number of controller nodes. Valid range depends on `form_factor`/`ssp_type`.",
				Optional:            true,
				Computed:            true,
			},
			"datacenter_id": schema.StringAttribute{
				MarkdownDescription: "The vSphere datacenter moref ID.",
				Required:            true,
			},
			"datacenter_name": schema.StringAttribute{
				MarkdownDescription: "The vSphere datacenter name.",
				Optional:            true,
			},
			"cluster_id": schema.StringAttribute{
				MarkdownDescription: "The vSphere cluster moref ID.",
				Required:            true,
			},
			"cluster_name": schema.StringAttribute{
				MarkdownDescription: "The vSphere cluster name.",
				Optional:            true,
			},
			"content_datastore_id": schema.StringAttribute{
				MarkdownDescription: "The vSphere content datastore moref ID.",
				Required:            true,
			},
			"datastore": schema.StringAttribute{
				MarkdownDescription: "The vSphere content datastore name.",
				Optional:            true,
			},
			"storage_policy_id": schema.StringAttribute{
				MarkdownDescription: "The vSphere storage policy ID.",
				Optional:            true,
			},
			"storage_policy_name": schema.StringAttribute{
				MarkdownDescription: "The vSphere storage policy name.",
				Optional:            true,
			},
			"vm_folder_name": schema.StringAttribute{
				MarkdownDescription: "The vSphere VM folder to place platform VMs into.",
				Optional:            true,
			},
			"vm_template": schema.StringAttribute{
				MarkdownDescription: "The VM template used to provision platform nodes.",
				Optional:            true,
			},
			"enable_resource_reservation": schema.BoolAttribute{
				MarkdownDescription: "Whether CPU and memory resources for the instance should be reserved at the resource pool level.",
				Optional:            true,
			},
			"domain": schema.StringAttribute{
				MarkdownDescription: "The search domain for the platform (skipped by DNS resolution).",
				Required:            true,
			},
			"dns_servers": schema.ListAttribute{
				MarkdownDescription: "The DNS servers for the cluster.",
				ElementType:         types.StringType,
				Required:            true,
			},
			"ntp_server": schema.StringAttribute{
				MarkdownDescription: "The NTP server for the cluster.",
				Required:            true,
			},
			"network_id": schema.StringAttribute{
				MarkdownDescription: "The virtual switch moref ID for platform cluster nodes.",
				Required:            true,
			},
			"network": schema.StringAttribute{
				MarkdownDescription: "The name of the virtual switch for platform cluster nodes.",
				Optional:            true,
			},
			"portgroup_id": schema.StringAttribute{
				MarkdownDescription: "The portgroup moref ID used for platform cluster nodes.",
				Required:            true,
			},
			"portgroup_name": schema.StringAttribute{
				MarkdownDescription: "The portgroup name used for platform cluster nodes.",
				Optional:            true,
			},
			"platform_default_gateway": schema.StringAttribute{
				MarkdownDescription: "The default gateway used by the platform nodes.",
				Required:            true,
			},
			"platform_subnet": schema.StringAttribute{
				MarkdownDescription: "The subnet where the cluster will run, in CIDR notation (e.g. `192.168.1.1/24`).",
				Required:            true,
			},
			"node_ip_pool": schema.ListAttribute{
				MarkdownDescription: "Node IP pool ranges, each formatted as `start-end` (e.g. `172.16.111.50-172.16.111.60`).",
				ElementType:         types.StringType,
				Required:            true,
			},
			"service_ip_pool": schema.ListAttribute{
				MarkdownDescription: "Service IP pool ranges, each formatted as `start-end` (e.g. `172.16.111.70-172.16.111.80`).",
				ElementType:         types.StringType,
				Required:            true,
			},
			"ingress_fqdn": schema.StringAttribute{
				MarkdownDescription: "The FQDN of the platform, associated with the ingress controller's external IP.",
				Required:            true,
			},
			"kafka_fqdn": schema.StringAttribute{
				MarkdownDescription: "The FQDN of Kafka running in the platform, associated with the `kafka-external` service IP.",
				Required:            true,
			},
			"ssp_bundle_id": schema.StringAttribute{
				MarkdownDescription: "The ID of the uploaded software bundle (`ssp_bundle_local`) to deploy.",
				Required:            true,
			},
			"ssp_bundle_version": schema.StringAttribute{
				MarkdownDescription: "The version of the software bundle to deploy.",
				Optional:            true,
			},
			"preserve_addons": schema.BoolAttribute{
				MarkdownDescription: "Whether to retain compatible add-ons across instance upgrades.",
				Optional:            true,
			},
			"addon_ids": schema.ListAttribute{
				MarkdownDescription: "The add-ons to attach to the platform.",
				ElementType:         types.StringType,
				Optional:            true,
			},
			"admin_password": schema.StringAttribute{
				MarkdownDescription: "The admin user password for the platform. If unset, the platform default is used.",
				Optional:            true,
				Sensitive:           true,
			},
			"audit_password": schema.StringAttribute{
				MarkdownDescription: "The audit user password for the platform. If unset, the platform default is used.",
				Optional:            true,
				Sensitive:           true,
			},
			"status": schema.StringAttribute{
				MarkdownDescription: "The current deployment status.",
				Computed:            true,
			},
		},
	}
}

func (r *PlatformResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

// parseIPPools converts a list of "start-end" formatted strings into IpPool entries.
func parseIPPools(ctx context.Context, l types.List, namePrefix string) ([]api_client.IpPool, diag.Diagnostics) {
	var diags diag.Diagnostics
	var entries []string
	diags.Append(l.ElementsAs(ctx, &entries, false)...)
	if diags.HasError() {
		return nil, diags
	}

	pools := make([]api_client.IpPool, 0, len(entries))
	for i, entry := range entries {
		parts := strings.SplitN(entry, "-", 2)
		if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
			diags.AddError(
				"Invalid IP Pool Range",
				fmt.Sprintf("Expected format \"start-end\" (e.g. \"172.16.111.50-172.16.111.60\"), got: %q", entry),
			)
			continue
		}
		pools = append(pools, api_client.IpPool{
			Name:  Ptr(fmt.Sprintf("%s-%d", namePrefix, i+1)),
			Start: strings.TrimSpace(parts[0]),
			End:   strings.TrimSpace(parts[1]),
		})
	}
	return pools, diags
}

func stringListOrEmpty(ctx context.Context, l types.List) ([]string, diag.Diagnostics) {
	if l.IsNull() || l.IsUnknown() {
		return nil, nil
	}
	var out []string
	diags := l.ElementsAs(ctx, &out, false)
	return out, diags
}

// buildPlatformPayload assembles the full API request body from the resource model.
func (r *PlatformResource) buildPlatformPayload(ctx context.Context, data *PlatformResourceModel, desiredState api_client.PlatformFullConfigDesiredState) (api_client.PlatformFullConfig, diag.Diagnostics) {
	var diags diag.Diagnostics

	sspTypeStr := "ATP"
	if !data.SspType.IsNull() && !data.SspType.IsUnknown() && data.SspType.ValueString() != "" {
		sspTypeStr = data.SspType.ValueString()
	}

	dnsServers, d := stringListOrEmpty(ctx, data.DnsServers)
	diags.Append(d...)

	nodePools, d := parseIPPools(ctx, data.NodeIPPool, "node-pool")
	diags.Append(d...)

	servicePools, d := parseIPPools(ctx, data.ServiceIPPool, "service-pool")
	diags.Append(d...)

	addonIDs, d := stringListOrEmpty(ctx, data.AddonIDs)
	diags.Append(d...)

	if diags.HasError() {
		return api_client.PlatformFullConfig{}, diags
	}

	formFactorVal := api_client.FormFactor(data.FormFactor.ValueString())

	var passwordConfig *api_client.SspPasswordConfig
	if !data.AdminPassword.IsNull() || !data.AuditPassword.IsNull() {
		passwordConfig = &api_client.SspPasswordConfig{}
		if !data.AdminPassword.IsNull() {
			passwordConfig.AdminPassword = Ptr(data.AdminPassword.ValueString())
		}
		if !data.AuditPassword.IsNull() {
			passwordConfig.AuditPassword = Ptr(data.AuditPassword.ValueString())
		}
	}

	var addonIDsPtr *[]string
	if addonIDs != nil {
		addonIDsPtr = &addonIDs
	}

	payload := api_client.PlatformFullConfig{
		ProviderId:   data.ProviderID.ValueString(),
		DesiredState: &desiredState,
		Compute: api_client.PlatformProviderConfig{
			DatacenterId:              data.DatacenterID.ValueString(),
			DatacenterName:            stringPtrOrNil(data.DatacenterName),
			ClusterId:                 data.ClusterID.ValueString(),
			ClusterName:               stringPtrOrNil(data.ClusterName),
			ContentDatastoreId:        data.ContentDatastoreID.ValueString(),
			ContentDatastoreName:      stringPtrOrNil(data.Datastore),
			StoragePolicyId:           stringValOrEmpty(data.StoragePolicyID),
			StoragePolicyName:         stringPtrOrNil(data.StoragePolicyName),
			VmFolderName:              stringPtrOrNil(data.VmFolderName),
			VmTemplate:                stringPtrOrNil(data.VmTemplate),
			EnableResourceReservation: boolPtrOrNil(data.EnableResourceReservation),
		},
		Network: api_client.PlatformNetwork{
			SearchDomain: data.Domain.ValueString(),
			Dns:          dnsServers,
			Ntp:          data.NtpServer.ValueString(),
			NetworkConfigs: []api_client.PlatformNetworkConfig{
				{
					NetworkId:              data.NetworkID.ValueString(),
					NetworkName:            stringPtrOrNil(data.Network),
					PortgroupId:            data.PortgroupID.ValueString(),
					PortgroupName:          stringPtrOrNil(data.PortgroupName),
					PlatformDefaultGateway: data.PlatformDefaultGateway.ValueString(),
					PlatformSubnet:         data.PlatformSubnet.ValueString(),
					NodePools:              nodePools,
					ServicePools:           servicePools,
				},
			},
		},
		Service: api_client.PlatformServiceConfig{
			InstanceName:          data.DisplayName.ValueString(),
			IngressFqdn:           data.IngressFqdn.ValueString(),
			KafkaFqdn:             data.KafkaFqdn.ValueString(),
			SspBundleId:           data.SspBundleID.ValueString(),
			SspBundleVersion:      stringPtrOrNil(data.SspBundleVersion),
			PasswordConfiguration: passwordConfig,
			PreserveAddons:        boolPtrOrNil(data.PreserveAddons),
			AddonIds:              addonIDsPtr,
		},
		System: api_client.PlatformSystemConfig{
			FormFactor:      &formFactorVal,
			ControllerCount: int64PtrToIntPtr(data.ControllerCount),
			WorkerCount:     int64PtrToIntPtr(data.WorkerCount),
			SspType:         Ptr(api_client.SspType(sspTypeStr)),
		},
	}

	return payload, diags
}

func stringPtrOrNil(v types.String) *string {
	if v.IsNull() || v.IsUnknown() || v.ValueString() == "" {
		return nil
	}
	return Ptr(v.ValueString())
}

func stringValOrEmpty(v types.String) string {
	if v.IsNull() || v.IsUnknown() {
		return ""
	}
	return v.ValueString()
}

func boolPtrOrNil(v types.Bool) *bool {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	return Ptr(v.ValueBool())
}

func int64PtrToIntPtr(v types.Int64) *int {
	if v.IsNull() || v.IsUnknown() {
		return nil
	}
	return Ptr(int(v.ValueInt64()))
}

// waitForWorkflow polls GET /sspi/platforms/{id}/status until the most recent
// workflow result reaches a terminal state, or until platformPollTimeout elapses.
func (r *PlatformResource) waitForWorkflow(ctx context.Context, platformID string) (*api_client.WorkflowResult, error) {
	deadline := time.Now().Add(platformPollTimeout)
	for {
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timed out after %s waiting for platform %s workflow to complete", platformPollTimeout, platformID)
		}

		statusResp, err := r.client.GetPlatformLcmStatusWithResponse(ctx, platformID)
		if err != nil {
			return nil, fmt.Errorf("error polling workflow status for platform %s: %w", platformID, err)
		}
		if statusResp.JSON200 == nil {
			return nil, fmt.Errorf("unexpected empty status response for platform %s (HTTP %d)", platformID, statusResp.StatusCode())
		}

		if results := statusResp.JSON200.WorkflowResults; results != nil && len(*results) > 0 {
			latest := (*results)[len(*results)-1]
			if latest.State != nil {
				switch *latest.State {
				case api_client.WorkflowResultStateCOMPLETED,
					api_client.WorkflowResultStateFAILED,
					api_client.WorkflowResultStateABORTED,
					api_client.WorkflowResultStateSTOPPED,
					api_client.WorkflowResultStateROLLBACKCOMPLETED,
					api_client.WorkflowResultStateROLLBACKFAILED,
					api_client.WorkflowResultStateROLLBACKSTOPPED:
					return &latest, nil
				}
			}
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(platformPollInterval):
		}
	}
}

// checkWorkflowResult returns a human-readable error if the workflow did not complete successfully.
func checkWorkflowResult(result *api_client.WorkflowResult) error {
	if result == nil || result.State == nil {
		return nil
	}
	switch *result.State {
	case api_client.WorkflowResultStateCOMPLETED, api_client.WorkflowResultStateROLLBACKCOMPLETED:
		return nil
	}

	name := "workflow"
	if result.DisplayName != nil {
		name = *result.DisplayName
	}
	details := ""
	if result.Jobs != nil {
		for _, job := range *result.Jobs {
			if job.State != nil && (*job.State == api_client.JobResultStateFAILED) {
				jobName := "job"
				if job.DisplayName != nil {
					jobName = *job.DisplayName
				}
				jobDetails := ""
				if job.Details != nil {
					jobDetails = ": " + *job.Details
				}
				details += fmt.Sprintf("\n  - %s failed%s", jobName, jobDetails)
			}
		}
	}
	return fmt.Errorf("%s ended in state %s%s", name, *result.State, details)
}

func (r *PlatformResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data PlatformResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, "Creating SSP Platform instance")

	platformLcmMu.Lock()
	defer platformLcmMu.Unlock()

	if data.SspType.IsNull() || data.SspType.IsUnknown() || data.SspType.ValueString() == "" {
		data.SspType = types.StringValue("ATP")
	}

	createPayload, diags := r.buildPlatformPayload(ctx, &data, api_client.START)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	createResp, err := r.client.CreatePlatformWithResponse(ctx, createPayload)
	if err != nil {
		resp.Diagnostics.AddError(
			"Error creating SSP Platform instance",
			"Could not create SSP Platform instance, unexpected error: "+err.Error(),
		)
		return
	}

	if createResp.JSON202 != nil && createResp.JSON202.Id != nil {
		data.ID = types.StringValue(*createResp.JSON202.Id)
	} else {
		resp.Diagnostics.AddError(
			"Error creating SSP Platform instance",
			fmt.Sprintf("Unexpected response from API: %d, body: %s", createResp.StatusCode(), string(createResp.Body)),
		)
		return
	}

	tflog.Debug(ctx, "Waiting for SSP Platform deployment workflow to complete", map[string]interface{}{"id": data.ID.ValueString()})
	result, err := r.waitForWorkflow(ctx, data.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(
			"Error waiting for SSP Platform deployment",
			err.Error(),
		)
		return
	}
	if err := checkWorkflowResult(result); err != nil {
		resp.Diagnostics.AddError(
			"SSP Platform deployment failed",
			err.Error(),
		)
		return
	}

	found := r.readPlatform(ctx, data.ID.ValueString(), &data, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if !found {
		resp.Diagnostics.AddError(
			"Error creating SSP Platform instance",
			"SSP Platform instance was created but could not be found immediately afterward.",
		)
		return
	}

	tflog.Trace(ctx, "Created SSP Platform instance", map[string]interface{}{"id": data.ID.ValueString()})
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *PlatformResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data PlatformResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	found := r.readPlatform(ctx, data.ID.ValueString(), &data, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if !found {
		resp.State.RemoveResource(ctx)
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// readPlatform reads the platform config into data, returning false (with no
// error) if the platform no longer exists (HTTP 404), so callers can decide
// whether that means "drop it from state" (Read) or "unexpected" (Create/Update).
func (r *PlatformResource) readPlatform(ctx context.Context, platformID string, data *PlatformResourceModel, diags *diag.Diagnostics) bool {
	tflog.Debug(ctx, "Reading SSP Platform instance", map[string]interface{}{"id": platformID})

	readResp, err := r.client.GetPlatformConfigWithResponse(ctx, platformID)
	if err != nil {
		diags.AddError(
			"Error reading SSP Platform instance",
			"Could not read SSP Platform instance, unexpected error: "+err.Error(),
		)
		return false
	}

	if readResp.StatusCode() == http.StatusNotFound {
		return false
	}

	if readResp.JSON200 == nil {
		diags.AddError(
			"Error reading SSP Platform instance",
			fmt.Sprintf("Unexpected response from API: %d", readResp.StatusCode()),
		)
		return false
	}

	cfg := readResp.JSON200

	data.ProviderID = types.StringValue(cfg.ProviderId)
	data.DatacenterID = types.StringValue(cfg.Compute.DatacenterId)
	data.ClusterID = types.StringValue(cfg.Compute.ClusterId)
	data.ContentDatastoreID = types.StringValue(cfg.Compute.ContentDatastoreId)
	data.DatacenterName = stringPtrToValue(cfg.Compute.DatacenterName)
	data.ClusterName = stringPtrToValue(cfg.Compute.ClusterName)
	data.Datastore = stringPtrToValue(cfg.Compute.ContentDatastoreName)
	if cfg.Compute.StoragePolicyId != "" {
		data.StoragePolicyID = types.StringValue(cfg.Compute.StoragePolicyId)
	} else {
		data.StoragePolicyID = types.StringNull()
	}
	data.StoragePolicyName = stringPtrToValue(cfg.Compute.StoragePolicyName)
	data.VmFolderName = stringPtrToValue(cfg.Compute.VmFolderName)
	data.VmTemplate = stringPtrToValue(cfg.Compute.VmTemplate)
	data.EnableResourceReservation = boolPtrToValue(cfg.Compute.EnableResourceReservation)

	if cfg.Network.SearchDomain != "" {
		data.Domain = types.StringValue(cfg.Network.SearchDomain)
	}
	if cfg.Network.Ntp != "" {
		data.NtpServer = types.StringValue(cfg.Network.Ntp)
	}
	if dnsList, d := types.ListValueFrom(ctx, types.StringType, cfg.Network.Dns); !d.HasError() {
		data.DnsServers = dnsList
	} else {
		diags.Append(d...)
	}
	if len(cfg.Network.NetworkConfigs) > 0 {
		nc := cfg.Network.NetworkConfigs[0]
		data.NetworkID = types.StringValue(nc.NetworkId)
		data.Network = stringPtrToValue(nc.NetworkName)
		data.PortgroupID = types.StringValue(nc.PortgroupId)
		data.PortgroupName = stringPtrToValue(nc.PortgroupName)
		data.PlatformDefaultGateway = types.StringValue(nc.PlatformDefaultGateway)
		data.PlatformSubnet = types.StringValue(nc.PlatformSubnet)
		data.NodeIPPool = ipPoolsToList(ctx, nc.NodePools, diags)
		data.ServiceIPPool = ipPoolsToList(ctx, nc.ServicePools, diags)
	}

	data.DisplayName = types.StringValue(cfg.Service.InstanceName)
	data.IngressFqdn = types.StringValue(cfg.Service.IngressFqdn)
	data.KafkaFqdn = types.StringValue(cfg.Service.KafkaFqdn)
	data.SspBundleID = types.StringValue(cfg.Service.SspBundleId)
	data.SspBundleVersion = stringPtrToValue(cfg.Service.SspBundleVersion)
	data.PreserveAddons = boolPtrToValue(cfg.Service.PreserveAddons)
	if cfg.Service.AddonIds != nil {
		if addonList, d := types.ListValueFrom(ctx, types.StringType, *cfg.Service.AddonIds); !d.HasError() {
			data.AddonIDs = addonList
		} else {
			diags.Append(d...)
		}
	} else {
		data.AddonIDs = types.ListNull(types.StringType)
	}
	// Passwords are write-only on the API and are intentionally left as-is
	// from prior state/config rather than overwritten here.

	if cfg.System.SspType != nil {
		data.SspType = types.StringValue(string(*cfg.System.SspType))
	}
	if cfg.System.FormFactor != nil {
		data.FormFactor = types.StringValue(string(*cfg.System.FormFactor))
	}
	if cfg.System.WorkerCount != nil {
		data.WorkerCount = types.Int64Value(int64(*cfg.System.WorkerCount))
	}
	if cfg.System.ControllerCount != nil {
		data.ControllerCount = types.Int64Value(int64(*cfg.System.ControllerCount))
	}

	statusResp, err := r.client.GetPlatformLcmStatusWithResponse(ctx, platformID)
	if err == nil && statusResp.JSON200 != nil {
		if statusResp.JSON200.Phase != nil {
			data.Status = types.StringValue(string(*statusResp.JSON200.Phase))
		}
	}

	return true
}

// stringPtrToValue converts an optional API string pointer to a Terraform
// string value, clearing to Null when the backend no longer returns it
// (instead of silently retaining a stale value from prior state).
func stringPtrToValue(v *string) types.String {
	if v == nil {
		return types.StringNull()
	}
	return types.StringValue(*v)
}

// boolPtrToValue converts an optional API bool pointer to a Terraform bool
// value, clearing to Null when the backend no longer returns it.
func boolPtrToValue(v *bool) types.Bool {
	if v == nil {
		return types.BoolNull()
	}
	return types.BoolValue(*v)
}

func ipPoolsToList(ctx context.Context, pools []api_client.IpPool, diags *diag.Diagnostics) types.List {
	entries := make([]string, 0, len(pools))
	for _, p := range pools {
		entries = append(entries, fmt.Sprintf("%s-%s", p.Start, p.End))
	}
	l, d := types.ListValueFrom(ctx, types.StringType, entries)
	diags.Append(d...)
	return l
}

func (r *PlatformResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var data PlatformResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, "Updating SSP Platform instance", map[string]interface{}{"id": data.ID.ValueString()})

	platformLcmMu.Lock()
	defer platformLcmMu.Unlock()

	if data.SspType.IsNull() || data.SspType.IsUnknown() || data.SspType.ValueString() == "" {
		data.SspType = types.StringValue("ATP")
	}

	// START saves the configuration and (re-)runs the precheck/LCM workflow;
	// on an already-deployed platform this drives the UPDATE_PRECHECK/UPDATE
	// phases rather than a fresh deployment.
	updatePayload, diags := r.buildPlatformPayload(ctx, &data, api_client.START)
	resp.Diagnostics.Append(diags...)
	if resp.Diagnostics.HasError() {
		return
	}

	updateResp, err := r.client.UpdatePlatformConfigWithResponse(ctx, data.ID.ValueString(), updatePayload)
	if err != nil {
		resp.Diagnostics.AddError(
			"Error updating SSP Platform instance",
			"Could not update SSP Platform instance: "+err.Error(),
		)
		return
	}

	if updateResp.StatusCode() != http.StatusOK && updateResp.StatusCode() != http.StatusAccepted {
		resp.Diagnostics.AddError(
			"Error updating SSP Platform instance",
			fmt.Sprintf("Unexpected response from API: %d, body: %s", updateResp.StatusCode(), string(updateResp.Body)),
		)
		return
	}

	tflog.Debug(ctx, "Waiting for SSP Platform update workflow to complete", map[string]interface{}{"id": data.ID.ValueString()})
	result, err := r.waitForWorkflow(ctx, data.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(
			"Error waiting for SSP Platform update",
			err.Error(),
		)
		return
	}
	if err := checkWorkflowResult(result); err != nil {
		resp.Diagnostics.AddError(
			"SSP Platform update failed",
			err.Error(),
		)
		return
	}

	found := r.readPlatform(ctx, data.ID.ValueString(), &data, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if !found {
		resp.Diagnostics.AddError(
			"Error updating SSP Platform instance",
			"SSP Platform instance was updated but could not be found immediately afterward.",
		)
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *PlatformResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data PlatformResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, "Deleting SSP Platform instance", map[string]interface{}{"id": data.ID.ValueString()})

	platformLcmMu.Lock()
	defer platformLcmMu.Unlock()

	deleteResp, err := r.client.DeletePlatformWithResponse(ctx, data.ID.ValueString(), &api_client.DeletePlatformParams{})
	if err != nil {
		resp.Diagnostics.AddError(
			"Error deleting SSP Platform instance",
			"Could not delete SSP Platform instance: "+err.Error(),
		)
		return
	}

	if deleteResp.StatusCode() != http.StatusOK && deleteResp.StatusCode() != http.StatusAccepted && deleteResp.StatusCode() != http.StatusNoContent && deleteResp.StatusCode() != http.StatusNotFound {
		resp.Diagnostics.AddError(
			"Error deleting SSP Platform instance",
			fmt.Sprintf("Unexpected response from API: %d", deleteResp.StatusCode()),
		)
		return
	}

	if deleteResp.StatusCode() == http.StatusNotFound {
		return
	}

	deadline := time.Now().Add(platformPollTimeout)
	for {
		if time.Now().After(deadline) {
			resp.Diagnostics.AddError(
				"Error deleting SSP Platform instance",
				fmt.Sprintf("timed out after %s waiting for platform %s teardown to complete", platformPollTimeout, data.ID.ValueString()),
			)
			return
		}

		readResp, err := r.client.GetPlatformConfigWithResponse(ctx, data.ID.ValueString())
		if err != nil {
			resp.Diagnostics.AddError(
				"Error polling SSP Platform teardown status",
				err.Error(),
			)
			return
		}
		if readResp.StatusCode() == http.StatusNotFound {
			return
		}

		select {
		case <-ctx.Done():
			resp.Diagnostics.AddError(
				"Error deleting SSP Platform instance",
				ctx.Err().Error(),
			)
			return
		case <-time.After(platformPollInterval):
		}
	}
}

func (r *PlatformResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
