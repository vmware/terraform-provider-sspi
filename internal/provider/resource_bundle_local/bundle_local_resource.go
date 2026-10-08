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

// uploadProgressPollInterval controls how often pollUploadProgressByName
// checks the appliance's own record of a still-uploading bundle.
var uploadProgressPollInterval = 15 * time.Second

// pollUploadProgressByName logs the appliance's own view of upload progress
// for the bundle named filename, until stop is closed. Bytes written into
// the local end of a tunneled/proxied connection can be accepted into local
// or transport buffers well before the appliance actually receives them -
// confirmed live: a client-side byte counter reported 100% sent while the
// appliance's own record showed only 65% actually received before the
// connection was later dropped. So progress is read from the appliance's
// bundle list (matched by display name, since the bundle ID isn't known
// until the upload request completes) rather than counted client-side.
func (r *BundleLocalResource) pollUploadProgressByName(ctx context.Context, filename string, stop <-chan struct{}) {
	ticker := time.NewTicker(uploadProgressPollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		listResp, err := r.client.GetBundlesWithResponse(ctx, &depot_client.GetBundlesParams{})
		if err != nil || listResp.JSON200 == nil || listResp.JSON200.Results == nil {
			continue
		}

		// Several bundles can share the same display name across retries;
		// take the most recently created one still actually uploading.
		var latest *depot_client.Bundle
		for _, b := range *listResp.JSON200.Results {
			if b.DisplayName == nil || *b.DisplayName != filename {
				continue
			}
			if b.Status == nil || *b.Status != depot_client.INPROGRESS {
				continue
			}
			if latest == nil || (b.UnderscoreCreateTime != nil && latest.UnderscoreCreateTime != nil && *b.UnderscoreCreateTime > *latest.UnderscoreCreateTime) {
				bCopy := b
				latest = &bCopy
			}
		}
		if latest == nil {
			continue
		}

		percent := 0
		if latest.Progress != nil {
			percent = *latest.Progress
		}
		tflog.Info(ctx, fmt.Sprintf("[%s] Upload progress: %d%%", filename, percent))
	}
}

// NewBundleLocalResource is a helper function to simplify the provider implementation.
func NewBundleLocalResource() resource.Resource {
	return &BundleLocalResource{}
}

// BundleLocalResource is the resource implementation.
type BundleLocalResource struct {
	client *depot_client.ClientWithResponses
	// uploadClient has a long HTTP timeout suited to multi-GB uploads; client
	// keeps the provider's short default for everything else.
	uploadClient *depot_client.ClientWithResponses
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
	r.uploadClient = r.client
	if up, ok := req.ProviderData.(interface {
		GetDepotUpload() *depot_client.ClientWithResponses
	}); ok && up.GetDepotUpload() != nil {
		r.uploadClient = up.GetDepotUpload()
	}
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

	// Uploads can take well over an hour on a slow/tunneled connection, during
	// which the only way to see real progress is the appliance's own record
	// (see pollUploadProgressByName) - there's no bundle ID to query directly
	// until this request completes, so it's found by matching filename.
	stopProgress := make(chan struct{})
	progressDone := make(chan struct{})
	go func() {
		defer close(progressDone)
		r.pollUploadProgressByName(ctx, filepath.Base(filePath), stopProgress)
	}()
	defer func() {
		close(stopProgress)
		<-progressDone
	}()

	uploadResp, err := r.uploadClient.UploadLocalBundleWithBodyWithResponse(
		ctx,
		&depot_client.UploadLocalBundleParams{},
		contentType,
		pr,
	)
	// When the server rejects the upload quickly (e.g. a bad bundle signature),
	// the HTTP client stops reading the request body as soon as it has the
	// response, which makes the writer goroutine's io.Copy above fail with a
	// generic "closed pipe" error. Check the real response first so that error
	// isn't reported instead of the server's actual diagnostic; only fall back
	// to it when there's no usable response to explain what happened.
	writeErr := <-writeErrCh
	if err != nil {
		resp.Diagnostics.AddError("Error uploading local bundle", err.Error())
		return
	}

	if uploadResp.StatusCode() != http.StatusOK && uploadResp.StatusCode() != http.StatusAccepted && uploadResp.StatusCode() != http.StatusCreated {
		resp.Diagnostics.AddError("Error uploading local bundle", fmt.Sprintf("Unexpected API response: %d, body: %s", uploadResp.StatusCode(), string(uploadResp.Body)))
		return
	}
	if writeErr != nil {
		resp.Diagnostics.AddError("Error uploading local bundle", writeErr.Error())
		return
	}

	// AsyncApiResponse.Id is a job/request-tracking identifier for this async
	// operation, NOT the depot bundle's own ID - confirmed live: polling
	// GET /sspi/bundles/{that id} 404s (same bug already found and fixed in
	// sspi_installer_bundle_remote). Use status_url instead, which is of the
	// form /sspi/bundles/{real-bundle-id}; extract the real ID from it so
	// polling/state/Read/Delete all target the actual bundle.
	if uploadResp.JSON202 == nil || uploadResp.JSON202.StatusUrl == nil {
		resp.Diagnostics.AddError(
			"Error uploading local bundle",
			fmt.Sprintf("Bundle upload was accepted (HTTP %d) but the API did not return a status_url: %s", uploadResp.StatusCode(), string(uploadResp.Body)),
		)
		return
	}
	bundleID := lastPathSegment(*uploadResp.JSON202.StatusUrl)
	if bundleID == "" {
		resp.Diagnostics.AddError(
			"Error uploading local bundle",
			fmt.Sprintf("Bundle upload was accepted (HTTP %d) but no bundle ID could be extracted from status_url %q", uploadResp.StatusCode(), *uploadResp.JSON202.StatusUrl),
		)
		return
	}
	data.ID = types.StringValue(bundleID)
	data.PackageID = types.StringValue(bundleID)

	data.Status = types.StringValue("UPLOADED")
	data.Version = types.StringNull()
	tflog.Trace(ctx, "Uploaded local SSPI bundle", map[string]interface{}{"id": data.ID.ValueString()})

	// The upload is async: file validation/extraction happens server-side
	// after the 202 response, with the bundle's status field settling to a
	// terminal state (READY, or a failure state). Wait for that before
	// reporting Create() success, so a downstream sspi_platform.ssp_bundle_id
	// reference in the same apply doesn't race a still-validating bundle.
	status, waitErr := r.waitForBundleReady(ctx, filepath.Base(filePath), data.ID.ValueString())
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
func (r *BundleLocalResource) waitForBundleReady(ctx context.Context, name, id string) (*depot_client.BundleStatus, error) {
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

		// Surfaces the appliance's own validation/processing progress (not just
		// "still connected") on the terminal with TF_LOG=INFO set.
		percent := 0
		if p := readResp.JSON200.Progress; p != nil {
			percent = *p
		}
		msg := fmt.Sprintf("[%s] Processing progress: %d%%", name, percent)
		if m := readResp.JSON200.Message; m != nil && *m != "" {
			msg += " - " + *m
		}
		tflog.Info(ctx, msg)

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
			fmt.Sprintf("Unexpected response from API: %d, body: %s", deleteResp.StatusCode(), string(deleteResp.Body)),
		)
		return
	}
}

func (r *BundleLocalResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
