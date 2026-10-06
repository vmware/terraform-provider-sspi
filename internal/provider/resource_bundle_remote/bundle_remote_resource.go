// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.

package provider

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/vmware/terraform-provider-sspi/internal/client/depot_client"
)

// bundleRemotePollInterval and bundleRemotePollTimeout are vars, not consts,
// so unit tests in this package can temporarily shrink them (save/restore)
// in milliseconds instead of the real 15s/90min production values.
var (
	bundleRemotePollInterval = 15 * time.Second
	bundleRemotePollTimeout  = 90 * time.Minute
)

// Ensure the implementation satisfies the expected interfaces.
var (
	_ resource.Resource                = &BundleRemoteResource{}
	_ resource.ResourceWithConfigure   = &BundleRemoteResource{}
	_ resource.ResourceWithImportState = &BundleRemoteResource{}
)

// NewBundleRemoteResource is a helper function to simplify the provider implementation.
func NewBundleRemoteResource() resource.Resource {
	return &BundleRemoteResource{}
}

// BundleRemoteResource is the resource implementation.
type BundleRemoteResource struct {
	client *depot_client.ClientWithResponses
}

// BundleRemoteResourceModel describes the resource data model.
type BundleRemoteResourceModel struct {
	ID        types.String `tfsdk:"id"`
	URL       types.String `tfsdk:"url"`
	Status    types.String `tfsdk:"status"`
	Version   types.String `tfsdk:"version"`
	PackageID types.String `tfsdk:"package_id"`
}

func (r *BundleRemoteResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_installer_bundle_remote"
}

func (r *BundleRemoteResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Imports a software bundle into the SSPI Depot from a remote URL - the " +
			"appliance itself downloads the file, so unlike `sspi_installer_bundle_local` no bundle " +
			"data is streamed through the machine running Terraform. Corresponds to `POST /sspi/bundles/remote`.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier for the imported bundle.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"url": schema.StringAttribute{
				MarkdownDescription: "HTTP(S) URL pointing to a valid bundle file. Must be reachable " +
					"from the SSPI appliance itself, without authentication.",
				Required: true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"status": schema.StringAttribute{
				MarkdownDescription: "The current status of the imported bundle.",
				Computed:            true,
			},
			"version": schema.StringAttribute{
				MarkdownDescription: "The version of the bundle.",
				Computed:            true,
			},
			"package_id": schema.StringAttribute{
				MarkdownDescription: "The package ID derived from the imported bundle.",
				Computed:            true,
			},
		},
	}
}

func (r *BundleRemoteResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	clientProvider, ok := req.ProviderData.(interface {
		GetDepot() *depot_client.ClientWithResponses
	})

	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Resource Configure Type",
			fmt.Sprintf("Expected provider data with GetDepot(), got: %T. Please report this issue to the provider developers.", req.ProviderData),
		)
		return
	}

	r.client = clientProvider.GetDepot()
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

func (r *BundleRemoteResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data BundleRemoteResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	url := data.URL.ValueString()
	tflog.Debug(ctx, "Importing remote SSPI bundle", map[string]any{"url": url})

	uploadResp, err := r.client.UploadRemoteBundleWithResponse(ctx, &depot_client.UploadRemoteBundleParams{}, depot_client.RemoteUpload{Url: url})
	if err != nil {
		resp.Diagnostics.AddError("Error importing remote bundle", err.Error())
		return
	}

	if uploadResp.StatusCode() != http.StatusOK && uploadResp.StatusCode() != http.StatusAccepted && uploadResp.StatusCode() != http.StatusCreated {
		resp.Diagnostics.AddError("Error importing remote bundle", fmt.Sprintf("Unexpected API response: %d, body: %s", uploadResp.StatusCode(), string(uploadResp.Body)))
		return
	}

	// AsyncApiResponse.Id is a job/request-tracking identifier for this async
	// operation, NOT the depot bundle's own ID - confirmed live: polling
	// GET /sspi/bundles/{that id} 404s. The API docs for this endpoint say to
	// use status_url instead ("a status URL pointing to the bundle resource"),
	// which is of the form /sspi/bundles/{real-bundle-id}; extract the real ID
	// from it so polling/state/Read/Delete all target the actual bundle.
	if uploadResp.JSON202 == nil || uploadResp.JSON202.StatusUrl == nil {
		resp.Diagnostics.AddError(
			"Error importing remote bundle",
			fmt.Sprintf("Bundle import was accepted (HTTP %d) but the API did not return a status_url: %s", uploadResp.StatusCode(), string(uploadResp.Body)),
		)
		return
	}
	bundleID := lastPathSegment(*uploadResp.JSON202.StatusUrl)
	if bundleID == "" {
		resp.Diagnostics.AddError(
			"Error importing remote bundle",
			fmt.Sprintf("Bundle import was accepted (HTTP %d) but no bundle ID could be extracted from status_url %q", uploadResp.StatusCode(), *uploadResp.JSON202.StatusUrl),
		)
		return
	}
	data.ID = types.StringValue(bundleID)
	data.PackageID = types.StringValue(bundleID)
	data.Status = types.StringValue("IN_PROGRESS")

	tflog.Trace(ctx, "Initiated remote SSPI bundle import", map[string]interface{}{"id": data.ID.ValueString()})

	// The import is async: the appliance downloads and validates the bundle
	// server-side after the 202 response, with the bundle's status field
	// settling to a terminal state (READY, or a failure state). Wait for that
	// before reporting Create() success, so a downstream
	// sspi_platform.ssp_bundle_id reference in the same apply doesn't race a
	// still-downloading/validating bundle.
	status, waitErr := r.waitForBundleReady(ctx, data.ID.ValueString())
	if status != nil {
		data.Status = types.StringValue(string(*status))
	}

	// Re-read to resolve the version (and confirm/refresh status and package_id)
	// now that the bundle exists, instead of leaving those Computed attributes
	// as guesses.
	r.readBundle(ctx, &data, &resp.Diagnostics)

	// Persist state now, regardless of the poll outcome: the bundle import was
	// genuinely initiated server-side, so losing track of its ID here would
	// cause the next apply to start a second, duplicate/orphaned import.
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
	if waitErr != nil {
		resp.Diagnostics.AddError("Bundle import did not complete successfully", waitErr.Error())
	}
}

// lastPathSegment returns the final non-empty segment of a URL path, e.g.
// "/sspi/bundles/abc-123" or "/sspi/bundles/abc-123/" -> "abc-123".
func lastPathSegment(u string) string {
	trimmed := strings.TrimRight(u, "/")
	idx := strings.LastIndex(trimmed, "/")
	if idx == -1 {
		return trimmed
	}
	return trimmed[idx+1:]
}

// waitForBundleReady polls GET /sspi/bundles/{id} until the bundle's status
// reaches a terminal state, returning an error if it failed.
func (r *BundleRemoteResource) waitForBundleReady(ctx context.Context, id string) (*depot_client.BundleStatus, error) {
	deadline := time.Now().Add(bundleRemotePollTimeout)
	for {
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timed out after %s waiting for bundle %s to become ready", bundleRemotePollTimeout, id)
		}

		readResp, err := r.client.GetBundleWithResponse(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("error polling status for bundle %s: %w", id, err)
		}
		if readResp.JSON200 == nil {
			return nil, fmt.Errorf("unexpected empty response polling status for bundle %s (HTTP %d)", id, readResp.StatusCode())
		}

		// Surfaces the appliance's own download/validation progress (not just
		// "still connected") on the terminal with TF_LOG=INFO set.
		percent := 0
		if p := readResp.JSON200.Progress; p != nil {
			percent = *p
		}
		msg := fmt.Sprintf("Processing progress: %d%%", percent)
		if m := readResp.JSON200.Message; m != nil && *m != "" {
			msg += " - " + *m
		}
		tflog.Info(ctx, msg)

		if status := readResp.JSON200.Status; status != nil && *status != depot_client.INPROGRESS {
			switch *status {
			case depot_client.FAILED, depot_client.ERROR, depot_client.CANCELLED:
				return status, fmt.Errorf("bundle %s import ended in status %s", id, *status)
			default:
				return status, nil
			}
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(bundleRemotePollInterval):
		}
	}
}

func (r *BundleRemoteResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data BundleRemoteResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	found := r.readBundle(ctx, &data, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}
	if !found {
		resp.State.RemoveResource(ctx)
		return
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

// readBundle reads the bundle into data, returning false (with no error) if
// the bundle no longer exists (HTTP 404).
func (r *BundleRemoteResource) readBundle(ctx context.Context, data *BundleRemoteResourceModel, diags *diag.Diagnostics) bool {
	tflog.Debug(ctx, "Reading SSPI Bundle Remote", map[string]interface{}{"id": data.ID.ValueString()})

	readResp, err := r.client.GetBundleWithResponse(ctx, data.ID.ValueString())
	if err != nil {
		diags.AddError(
			"Error reading SSPI Bundle Remote",
			"Could not read SSPI Bundle Remote: "+err.Error(),
		)
		return false
	}

	if readResp.StatusCode() == http.StatusNotFound {
		return false
	}

	if readResp.JSON200 == nil {
		diags.AddError(
			"Error reading SSPI Bundle Remote",
			fmt.Sprintf("Unexpected response from API: %d, body: %s", readResp.StatusCode(), string(readResp.Body)),
		)
		return false
	}

	bundle := readResp.JSON200
	if bundle.Status != nil {
		data.Status = types.StringValue(string(*bundle.Status))
	} else {
		data.Status = types.StringNull()
	}
	if bundle.BundleVersion != nil {
		data.Version = types.StringValue(*bundle.BundleVersion)
	} else {
		data.Version = types.StringNull()
	}
	if bundle.Id != nil {
		data.PackageID = types.StringValue(*bundle.Id)
	}
	return true
}

func (r *BundleRemoteResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError(
		"Update Not Supported",
		"Updating an imported remote bundle is not supported. Please recreate the resource.",
	)
}

func (r *BundleRemoteResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data BundleRemoteResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, "Deleting SSPI Bundle Remote", map[string]interface{}{"id": data.ID.ValueString()})

	deleteResp, err := r.client.DeleteBundleWithResponse(ctx, data.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(
			"Error deleting SSPI Bundle Remote",
			"Could not delete SSPI Bundle Remote: "+err.Error(),
		)
		return
	}

	if deleteResp.StatusCode() != http.StatusOK && deleteResp.StatusCode() != http.StatusNoContent && deleteResp.StatusCode() != http.StatusNotFound {
		resp.Diagnostics.AddError(
			"Error deleting SSPI Bundle Remote",
			fmt.Sprintf("Unexpected response from API: %d, body: %s", deleteResp.StatusCode(), string(deleteResp.Body)),
		)
		return
	}
}

func (r *BundleRemoteResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
