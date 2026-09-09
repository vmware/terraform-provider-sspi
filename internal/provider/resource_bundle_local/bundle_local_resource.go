// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.

package provider

import (
	"context"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
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

// bundlePollInterval and bundlePollTimeout are vars, not consts, so unit
// tests in this package can temporarily shrink them (save/restore) to
// exercise the multi-iteration polling loop and deadline logic in
// waitForBundleReady in milliseconds instead of the real 15s/90min
// production values.
var (
	bundlePollInterval = 15 * time.Second
	bundlePollTimeout  = 90 * time.Minute
)

// Ensure the implementation satisfies the expected interfaces.
var (
	_ resource.Resource                = &BundleLocalResource{}
	_ resource.ResourceWithConfigure   = &BundleLocalResource{}
	_ resource.ResourceWithImportState = &BundleLocalResource{}
)

// NewBundleLocalResource is a helper function to simplify the provider implementation.
func NewBundleLocalResource() resource.Resource {
	return &BundleLocalResource{}
}

// BundleLocalResource is the resource implementation.
type BundleLocalResource struct {
	client *depot_client.ClientWithResponses
}

// BundleLocalResourceModel describes the resource data model.
type BundleLocalResourceModel struct {
	ID        types.String `tfsdk:"id"`
	FilePath  types.String `tfsdk:"file_path"`
	Status    types.String `tfsdk:"status"`
	Version   types.String `tfsdk:"version"`
	PackageID types.String `tfsdk:"package_id"`
}

func (r *BundleLocalResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_installer_bundle_local"
}

func (r *BundleLocalResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages an uploaded local software bundle in the SSPI Depot.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier for the uploaded bundle.",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"file_path": schema.StringAttribute{
				MarkdownDescription: "The absolute local path to the bundle file to upload.",
				Required:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.RequiresReplace(),
				},
			},
			"status": schema.StringAttribute{
				MarkdownDescription: "The current status of the uploaded bundle.",
				Computed:            true,
			},
			"version": schema.StringAttribute{
				MarkdownDescription: "The version of the bundle.",
				Computed:            true,
			},
			"package_id": schema.StringAttribute{
				MarkdownDescription: "The package ID derived from the uploaded bundle.",
				Computed:            true,
			},
		},
	}
}

func (r *BundleLocalResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *BundleLocalResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data BundleLocalResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	filePath := data.FilePath.ValueString()
	tflog.Debug(ctx, "Uploading local SSPI bundle", map[string]any{"path": filePath})

	file, err := os.Open(filePath)
	if err != nil {
		resp.Diagnostics.AddError("Error opening local bundle file", fmt.Sprintf("Could not open file %s: %s", filePath, err.Error()))
		return
	}
	defer func() { _ = file.Close() }()

	// Stream the multipart body through an io.Pipe so the full file is never
	// buffered in memory (bundles can be several GB).
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	contentType := mw.FormDataContentType()

	writeErrCh := make(chan error, 1)
	go func() {
		defer close(writeErrCh)
		fw, err := mw.CreateFormFile("file", filepath.Base(filePath))
		if err != nil {
			writeErrCh <- fmt.Errorf("error creating multipart form file: %w", err)
			pw.CloseWithError(err)
			return
		}
		if _, err := io.Copy(fw, file); err != nil {
			writeErrCh <- fmt.Errorf("error writing file to multipart form: %w", err)
			pw.CloseWithError(err)
			return
		}
		if err := mw.Close(); err != nil {
			writeErrCh <- fmt.Errorf("error closing multipart writer: %w", err)
			pw.CloseWithError(err)
			return
		}
		pw.Close()
	}()

	uploadResp, err := r.client.UploadLocalBundleWithBodyWithResponse(
		ctx,
		&depot_client.UploadLocalBundleParams{},
		contentType,
		pr,
	)
	if writeErr := <-writeErrCh; writeErr != nil {
		resp.Diagnostics.AddError("Error uploading local bundle", writeErr.Error())
		return
	}
	if err != nil {
		resp.Diagnostics.AddError("Error uploading local bundle", err.Error())
		return
	}

	if uploadResp.StatusCode() != http.StatusOK && uploadResp.StatusCode() != http.StatusAccepted && uploadResp.StatusCode() != http.StatusCreated {
		resp.Diagnostics.AddError("Error uploading local bundle", fmt.Sprintf("Unexpected API response: %d", uploadResp.StatusCode()))
		return
	}

	if uploadResp.JSON202 == nil || uploadResp.JSON202.Id == nil {
		// Falling back to a locally-derived ID (e.g. the filename) would create a
		// resource whose ID the Depot doesn't recognize, guaranteeing a 404 on the
		// very next Read/Delete; fail loudly instead so the upload can be retried.
		resp.Diagnostics.AddError(
			"Error uploading local bundle",
			fmt.Sprintf("Bundle upload was accepted (HTTP %d) but the API did not return a bundle ID: %s", uploadResp.StatusCode(), string(uploadResp.Body)),
		)
		return
	}
	data.ID = types.StringValue(*uploadResp.JSON202.Id)
	data.PackageID = types.StringValue(*uploadResp.JSON202.Id)

	data.Status = types.StringValue("UPLOADED")
	tflog.Trace(ctx, "Uploaded local SSPI bundle", map[string]interface{}{"id": data.ID.ValueString()})

	// The upload is async: file validation/extraction happens server-side
	// after the 202 response, with the bundle's status field settling to a
	// terminal state (READY, or a failure state). Wait for that before
	// reporting Create() success, so a downstream sspi_platform.ssp_bundle_id
	// reference in the same apply doesn't race a still-validating bundle.
	status, waitErr := r.waitForBundleReady(ctx, data.ID.ValueString())
	if status != nil {
		data.Status = types.StringValue(string(*status))
	}

	// Re-read to resolve the version (and confirm/refresh status and package_id)
	// now that the bundle exists, instead of leaving those Computed attributes
	// as guesses.
	r.readBundle(ctx, &data, &resp.Diagnostics)

	// Persist state now, regardless of the poll outcome: the bundle was
	// genuinely created server-side, so losing track of its ID here would
	// cause the next apply to upload a second, duplicate/orphaned bundle.
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
	if waitErr != nil {
		resp.Diagnostics.AddError("Bundle upload did not complete successfully", waitErr.Error())
	}
}

// waitForBundleReady polls GET /sspi/bundles/{id} until the bundle's status
// reaches a terminal state, returning an error if it failed.
func (r *BundleLocalResource) waitForBundleReady(ctx context.Context, id string) (*depot_client.BundleStatus, error) {
	deadline := time.Now().Add(bundlePollTimeout)
	for {
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("timed out after %s waiting for bundle %s to become ready", bundlePollTimeout, id)
		}

		readResp, err := r.client.GetBundleWithResponse(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("error polling status for bundle %s: %w", id, err)
		}
		if readResp.JSON200 == nil {
			return nil, fmt.Errorf("unexpected empty response polling status for bundle %s (HTTP %d)", id, readResp.StatusCode())
		}

		if status := readResp.JSON200.Status; status != nil && *status != depot_client.INPROGRESS {
			switch *status {
			case depot_client.FAILED, depot_client.ERROR, depot_client.CANCELLED:
				return status, fmt.Errorf("bundle %s upload ended in status %s", id, *status)
			default:
				return status, nil
			}
		}

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(bundlePollInterval):
		}
	}
}

func (r *BundleLocalResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data BundleLocalResourceModel

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
func (r *BundleLocalResource) readBundle(ctx context.Context, data *BundleLocalResourceModel, diags *diag.Diagnostics) bool {
	tflog.Debug(ctx, "Reading SSPI Bundle Local", map[string]interface{}{"id": data.ID.ValueString()})

	readResp, err := r.client.GetBundleWithResponse(ctx, data.ID.ValueString())
	if err != nil {
		diags.AddError(
			"Error reading SSPI Bundle Local",
			"Could not read SSPI Bundle Local: "+err.Error(),
		)
		return false
	}

	if readResp.StatusCode() == http.StatusNotFound {
		return false
	}

	if readResp.JSON200 == nil {
		diags.AddError(
			"Error reading SSPI Bundle Local",
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

func (r *BundleLocalResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	resp.Diagnostics.AddError(
		"Update Not Supported",
		"Updating an uploaded local bundle is not supported. Please recreate the resource.",
	)
}

func (r *BundleLocalResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data BundleLocalResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, "Deleting SSPI Bundle Local", map[string]interface{}{"id": data.ID.ValueString()})

	deleteResp, err := r.client.DeleteBundleWithResponse(ctx, data.ID.ValueString())
	if err != nil {
		resp.Diagnostics.AddError(
			"Error deleting SSPI Bundle Local",
			"Could not delete SSPI Bundle Local: "+err.Error(),
		)
		return
	}

	if deleteResp.StatusCode() != http.StatusOK && deleteResp.StatusCode() != http.StatusNoContent && deleteResp.StatusCode() != http.StatusNotFound {
		resp.Diagnostics.AddError(
			"Error deleting SSPI Bundle Local",
			fmt.Sprintf("Unexpected response from API: %d", deleteResp.StatusCode()),
		)
		return
	}
}

func (r *BundleLocalResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
