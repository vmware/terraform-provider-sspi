// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.

package provider

import (
	"context"
	"fmt"
	"net/http"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/path"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/int64default"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/objectplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/planmodifier"
	"github.com/hashicorp/terraform-plugin-framework/resource/schema/stringplanmodifier"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-framework/types/basetypes"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/vmware/terraform-provider-sspi/internal/client/api_client"
)

var installerObjectAsOptions = basetypes.ObjectAsOptions{UnhandledNullAsEmpty: true, UnhandledUnknownAsEmpty: true}

var installerWeeklyScheduleAttrTypes = map[string]attr.Type{
	"days_of_week":  types.ListType{ElemType: types.StringType},
	"hour_of_day":   types.Int64Type,
	"minute_of_day": types.Int64Type,
}

var installerIntervalScheduleAttrTypes = map[string]attr.Type{
	"hours_between_backups": types.Int64Type,
}

// Ensure the implementation satisfies the expected interfaces.
var (
	_ resource.Resource                = &RecurringBackupConfigResource{}
	_ resource.ResourceWithConfigure   = &RecurringBackupConfigResource{}
	_ resource.ResourceWithImportState = &RecurringBackupConfigResource{}
)

func NewRecurringBackupConfigResource() resource.Resource {
	return &RecurringBackupConfigResource{}
}

type RecurringBackupConfigResource struct {
	client *api_client.ClientWithResponses
}

type RecurringBackupConfigResourceModel struct {
	ID                     types.String `tfsdk:"id"`
	Enabled                types.Bool   `tfsdk:"enabled"`
	BackupType             types.String `tfsdk:"backup_type"`
	BackupScheduleType     types.String `tfsdk:"backup_schedule_type"`
	BackupScheduleWeekly   types.Object `tfsdk:"backup_schedule_weekly"`
	BackupScheduleInterval types.Object `tfsdk:"backup_schedule_interval"`
}

func (r *RecurringBackupConfigResource) Metadata(ctx context.Context, req resource.MetadataRequest, resp *resource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_installer_recurring_backup_config"
}

func (r *RecurringBackupConfigResource) Schema(ctx context.Context, req resource.SchemaRequest, resp *resource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Manages automated recurring backup schedule for SSPI.",
		Attributes: map[string]schema.Attribute{
			"id": schema.StringAttribute{
				MarkdownDescription: "Unique identifier for recurring backup configuration (always 'singleton').",
				Computed:            true,
				PlanModifiers: []planmodifier.String{
					stringplanmodifier.UseStateForUnknown(),
				},
			},
			"enabled": schema.BoolAttribute{
				MarkdownDescription: "Whether automated recurring backups are enabled.",
				Required:            true,
			},
			"backup_type": schema.StringAttribute{
				MarkdownDescription: "Type of backup (e.g. FULL_BACKUP).",
				Optional:            true,
			},
			"backup_schedule_type": schema.StringAttribute{
				MarkdownDescription: "Schedule type: `WEEKLY` (calendar days/time) or `INTERVAL` (hours between runs).",
				Optional:            true,
			},
			"backup_schedule_weekly": schema.SingleNestedAttribute{
				Optional: true,
				Computed: true,
				MarkdownDescription: "Weekly schedule settings. Required when `backup_schedule_type = WEEKLY` — " +
					"the live API rejects `WEEKLY` without it (`\"weekly backup schedule not found for schedule " +
					"type WEEKLY\"`).",
				PlanModifiers: []planmodifier.Object{objectplanmodifier.UseStateForUnknown()},
				Attributes: map[string]schema.Attribute{
					"days_of_week": schema.ListAttribute{
						Required:            true,
						ElementType:         types.StringType,
						MarkdownDescription: "Days of week when backup runs (e.g. `[\"MONDAY\", \"THURSDAY\"]`).",
					},
					"hour_of_day": schema.Int64Attribute{
						Optional:            true,
						Computed:            true,
						Default:             int64default.StaticInt64(0),
						MarkdownDescription: "Hour of day (0-23) when the backup starts. Defaults to `0`.",
					},
					"minute_of_day": schema.Int64Attribute{
						Optional:            true,
						Computed:            true,
						Default:             int64default.StaticInt64(0),
						MarkdownDescription: "Minute within the hour (0-59) when the backup starts. Defaults to `0`.",
					},
				},
			},
			"backup_schedule_interval": schema.SingleNestedAttribute{
				Optional:            true,
				Computed:            true,
				MarkdownDescription: "Interval schedule settings. Required when `backup_schedule_type = INTERVAL`.",
				PlanModifiers:       []planmodifier.Object{objectplanmodifier.UseStateForUnknown()},
				Attributes: map[string]schema.Attribute{
					"hours_between_backups": schema.Int64Attribute{
						Optional:            true,
						Computed:            true,
						Default:             int64default.StaticInt64(168),
						MarkdownDescription: "Hours between consecutive automated backups. Defaults to `168` (weekly).",
					},
				},
			},
		},
	}
}

func (r *RecurringBackupConfigResource) Configure(ctx context.Context, req resource.ConfigureRequest, resp *resource.ConfigureResponse) {
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

func (r *RecurringBackupConfigResource) Create(ctx context.Context, req resource.CreateRequest, resp *resource.CreateResponse) {
	var data RecurringBackupConfigResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, "Creating SSPI Recurring Backup Configuration")

	r.saveRecurringConfig(ctx, &data, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	data.ID = types.StringValue("singleton")
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *RecurringBackupConfigResource) saveRecurringConfig(ctx context.Context, data *RecurringBackupConfigResourceModel, diags *diag.Diagnostics) {
	bt := api_client.FULLBACKUP
	if !data.BackupType.IsNull() {
		bt = api_client.BackupType(data.BackupType.ValueString())
	}

	st := api_client.WEEKLY
	if !data.BackupScheduleType.IsNull() {
		st = api_client.BackupScheduleType(data.BackupScheduleType.ValueString())
	}

	body := api_client.RecurringBackupConfig{
		BackupScheduleType: st,
		BackupType:         bt,
		Enabled:            data.Enabled.ValueBool(),
	}

	if !data.BackupScheduleWeekly.IsNull() && !data.BackupScheduleWeekly.IsUnknown() {
		var weekly struct {
			DaysOfWeek  types.List  `tfsdk:"days_of_week"`
			HourOfDay   types.Int64 `tfsdk:"hour_of_day"`
			MinuteOfDay types.Int64 `tfsdk:"minute_of_day"`
		}
		diags.Append(data.BackupScheduleWeekly.As(ctx, &weekly, installerObjectAsOptions)...)
		if diags.HasError() {
			return
		}
		var days []string
		diags.Append(weekly.DaysOfWeek.ElementsAs(ctx, &days, false)...)
		if diags.HasError() {
			return
		}
		weekDays := make([]api_client.WeekDay, len(days))
		for i, d := range days {
			weekDays[i] = api_client.WeekDay(d)
		}
		hourOfDay := int(weekly.HourOfDay.ValueInt64())
		minuteOfDay := int(weekly.MinuteOfDay.ValueInt64())
		body.BackupScheduleWeekly = &api_client.WeeklyBackupSchedule{
			DaysOfWeek:  weekDays,
			HourOfDay:   &hourOfDay,
			MinuteOfDay: &minuteOfDay,
		}
	}

	if !data.BackupScheduleInterval.IsNull() && !data.BackupScheduleInterval.IsUnknown() {
		var interval struct {
			HoursBetweenBackups types.Int64 `tfsdk:"hours_between_backups"`
		}
		diags.Append(data.BackupScheduleInterval.As(ctx, &interval, installerObjectAsOptions)...)
		if diags.HasError() {
			return
		}
		hours := int(interval.HoursBetweenBackups.ValueInt64())
		body.BackupScheduleInterval = &api_client.IntervalBackupSchedule{
			HoursBetweenBackups: &hours,
		}
	}

	// Fetch the current revision (if a recurring backup config already exists)
	// so this PUT isn't rejected by the backend's optimistic-locking check; a
	// 404 means no config exists yet, so the zero-value Revision is correct.
	currentResp, err := r.client.GetRecurringBackupConfigWithResponse(ctx)
	if err != nil {
		diags.AddError("Error reading current Recurring Backup Configuration", "Could not read current recurring backup config: "+err.Error())
		return
	}
	if currentResp.StatusCode() != http.StatusNotFound {
		if currentResp.JSON200 == nil {
			diags.AddError("Error reading current Recurring Backup Configuration",
				fmt.Sprintf("Unexpected API response: %d: %s", currentResp.StatusCode(), string(currentResp.Body)))
			return
		}
		body.UnderscoreRevision = currentResp.JSON200.UnderscoreRevision
	}

	updateResp, err := r.client.UpdateRecurringBackupConfigWithResponse(ctx, body)
	if err != nil {
		diags.AddError("Error saving Recurring Backup Configuration", "Could not save recurring backup config: "+err.Error())
		return
	}

	if updateResp.StatusCode() != http.StatusOK && updateResp.StatusCode() != http.StatusAccepted {
		diags.AddError("Error saving Recurring Backup Configuration",
			fmt.Sprintf("Unexpected API response: %d: %s", updateResp.StatusCode(), string(updateResp.Body)))
		return
	}

	if updateResp.JSON200 != nil {
		mapRecurringConfigToState(updateResp.JSON200, data, diags)
		return
	}

	// PUT was accepted (202) without a parsed body — re-read to resolve all
	// Computed attributes to known values, since Terraform disallows an Unknown
	// value remaining in state after apply.
	readResp, err := r.client.GetRecurringBackupConfigWithResponse(ctx)
	if err != nil {
		diags.AddError("Error reading Recurring Backup Configuration after save", err.Error())
		return
	}
	if readResp.JSON200 != nil {
		mapRecurringConfigToState(readResp.JSON200, data, diags)
	}
}

func mapRecurringConfigToState(cfg *api_client.RecurringBackupConfig, data *RecurringBackupConfigResourceModel, diags *diag.Diagnostics) {
	data.Enabled = types.BoolValue(cfg.Enabled)
	data.BackupType = types.StringValue(string(cfg.BackupType))
	data.BackupScheduleType = types.StringValue(string(cfg.BackupScheduleType))

	if cfg.BackupScheduleWeekly != nil {
		days := make([]attr.Value, len(cfg.BackupScheduleWeekly.DaysOfWeek))
		for i, d := range cfg.BackupScheduleWeekly.DaysOfWeek {
			days[i] = types.StringValue(string(d))
		}
		daysList, d := types.ListValue(types.StringType, days)
		diags.Append(d...)
		hourOfDay := 0
		if cfg.BackupScheduleWeekly.HourOfDay != nil {
			hourOfDay = *cfg.BackupScheduleWeekly.HourOfDay
		}
		minuteOfDay := 0
		if cfg.BackupScheduleWeekly.MinuteOfDay != nil {
			minuteOfDay = *cfg.BackupScheduleWeekly.MinuteOfDay
		}
		weeklyObj, d := types.ObjectValue(installerWeeklyScheduleAttrTypes, map[string]attr.Value{
			"days_of_week":  daysList,
			"hour_of_day":   types.Int64Value(int64(hourOfDay)),
			"minute_of_day": types.Int64Value(int64(minuteOfDay)),
		})
		diags.Append(d...)
		data.BackupScheduleWeekly = weeklyObj
	} else {
		data.BackupScheduleWeekly = types.ObjectNull(installerWeeklyScheduleAttrTypes)
	}

	if cfg.BackupScheduleInterval != nil {
		hours := 0
		if cfg.BackupScheduleInterval.HoursBetweenBackups != nil {
			hours = *cfg.BackupScheduleInterval.HoursBetweenBackups
		}
		intervalObj, d := types.ObjectValue(installerIntervalScheduleAttrTypes, map[string]attr.Value{
			"hours_between_backups": types.Int64Value(int64(hours)),
		})
		diags.Append(d...)
		data.BackupScheduleInterval = intervalObj
	} else {
		data.BackupScheduleInterval = types.ObjectNull(installerIntervalScheduleAttrTypes)
	}
}

func (r *RecurringBackupConfigResource) Read(ctx context.Context, req resource.ReadRequest, resp *resource.ReadResponse) {
	var data RecurringBackupConfigResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, "Reading SSPI Recurring Backup Configuration")

	readResp, err := r.client.GetRecurringBackupConfigWithResponse(ctx)
	if err != nil {
		resp.Diagnostics.AddError(
			"Error reading SSPI Recurring Backup Configuration",
			"Could not read SSPI Recurring Backup Configuration, unexpected error: "+err.Error(),
		)
		return
	}

	if readResp.StatusCode() == http.StatusNotFound {
		resp.State.RemoveResource(ctx)
		return
	}

	if readResp.JSON200 == nil {
		resp.Diagnostics.AddError(
			"Error reading SSPI Recurring Backup Configuration",
			fmt.Sprintf("Unexpected response from API: %d, body: %s", readResp.StatusCode(), string(readResp.Body)),
		)
		return
	}

	mapRecurringConfigToState(readResp.JSON200, &data, &resp.Diagnostics)

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *RecurringBackupConfigResource) Update(ctx context.Context, req resource.UpdateRequest, resp *resource.UpdateResponse) {
	var data RecurringBackupConfigResourceModel

	resp.Diagnostics.Append(req.Plan.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, "Updating SSPI Recurring Backup Configuration")

	r.saveRecurringConfig(ctx, &data, &resp.Diagnostics)
	if resp.Diagnostics.HasError() {
		return
	}

	data.ID = types.StringValue("singleton")
	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}

func (r *RecurringBackupConfigResource) Delete(ctx context.Context, req resource.DeleteRequest, resp *resource.DeleteResponse) {
	var data RecurringBackupConfigResourceModel

	resp.Diagnostics.Append(req.State.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	tflog.Debug(ctx, "Deleting SSPI Recurring Backup Configuration")
}

func (r *RecurringBackupConfigResource) ImportState(ctx context.Context, req resource.ImportStateRequest, resp *resource.ImportStateResponse) {
	resource.ImportStatePassthroughID(ctx, path.Root("id"), req, resp)
}
