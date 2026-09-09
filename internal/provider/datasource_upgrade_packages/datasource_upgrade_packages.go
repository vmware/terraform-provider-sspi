// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.

package provider

import (
	"context"
	"fmt"
	"net/http"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/datasource/schema"
	"github.com/hashicorp/terraform-plugin-framework/types"
	"github.com/hashicorp/terraform-plugin-log/tflog"

	"github.com/vmware/terraform-provider-sspi/internal/client/upgrade_client"
)

// Ensure provider defined types fully satisfy framework interfaces.
var _ datasource.DataSource = &UpgradePackagesDataSource{}

func NewUpgradePackagesDataSource() datasource.DataSource {
	return &UpgradePackagesDataSource{}
}

// UpgradePackagesDataSource lists the upgrade packages the SSPI appliance
// considers eligible for its own self-upgrade -- the "List packages
// available for upgrade" PRD requirement for the Upgrade workflow.
type UpgradePackagesDataSource struct {
	client *upgrade_client.ClientWithResponses
}

type UpgradePackagesDataSourceModel struct {
	Packages []UpgradePackageModel `tfsdk:"packages"`
}

type UpgradePackageModel struct {
	PackageName types.String `tfsdk:"package_name"`
	PackageSize types.String `tfsdk:"package_size"`
	Version     types.String `tfsdk:"version"`
}

func (d *UpgradePackagesDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_upgrade_packages"
}

func (d *UpgradePackagesDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Lists the upgrade packages the SSPI appliance depot currently considers " +
			"eligible for appliance self-upgrade (`sspi_upgrade`).\n\n" +
			"Corresponds to `GET /sspi/upgrade/packages`.",
		Attributes: map[string]schema.Attribute{
			"packages": schema.ListNestedAttribute{
				Computed:            true,
				MarkdownDescription: "List of upgrade packages eligible for appliance upgrade.",
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"package_name": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "Filename of the upgrade bundle (e.g. `upgrade-bundle-5.1.0.0.0.29219600.sub`).",
						},
						"package_size": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "Human-readable size of the upgrade bundle (e.g. `4.94G`).",
						},
						"version": schema.StringAttribute{
							Computed:            true,
							MarkdownDescription: "Version contained in this upgrade package (e.g. `5.1.0.0.0.29219600`).",
						},
					},
				},
			},
		},
	}
}

func (d *UpgradePackagesDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	clientProvider, ok := req.ProviderData.(interface {
		GetUpgrade() *upgrade_client.ClientWithResponses
	})
	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Data Source Configure Type",
			fmt.Sprintf("Expected provider data with GetUpgrade(), got: %T.", req.ProviderData),
		)
		return
	}

	d.client = clientProvider.GetUpgrade()
	if d.client == nil {
		resp.Diagnostics.AddError(
			"SSPI Appliance Not Configured",
			"This data source requires the SSPI appliance connection to be configured on the provider "+
				"(\"sspi_host\", \"sspi_username\", and \"sspi_password\", or the SSPI_HOST, SSPI_USERNAME, "+
				"and SSPI_PASSWORD environment variables).",
		)
		return
	}
}

func (d *UpgradePackagesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	tflog.Debug(ctx, "Reading SSPI Upgrade Packages Data Source")

	readResp, err := d.client.GetUpgradePackagesWithResponse(ctx)
	if err != nil {
		resp.Diagnostics.AddError(
			"Error Reading SSPI Upgrade Packages Data Source",
			"Could not list upgrade packages: "+err.Error(),
		)
		return
	}

	if readResp.StatusCode() != http.StatusOK || readResp.JSON200 == nil {
		resp.Diagnostics.AddError(
			"Error Reading SSPI Upgrade Packages Data Source",
			fmt.Sprintf("Unexpected response status code: %d", readResp.StatusCode()),
		)
		return
	}

	var data UpgradePackagesDataSourceModel
	data.Packages = make([]UpgradePackageModel, len(readResp.JSON200.Results))
	for i, pkg := range readResp.JSON200.Results {
		data.Packages[i] = UpgradePackageModel{
			PackageName: types.StringValue(pkg.PackageName),
			PackageSize: types.StringValue(pkg.PackageSize),
			Version:     types.StringValue(pkg.Version),
		}
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
