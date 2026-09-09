// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.

package provider

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/hashicorp/terraform-plugin-framework/datasource"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/function"
	"github.com/hashicorp/terraform-plugin-framework/provider"
	"github.com/hashicorp/terraform-plugin-framework/provider/schema"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"

	"golang.org/x/net/proxy"

	"github.com/vmware/terraform-provider-sspi/internal/client/api_client"
	"github.com/vmware/terraform-provider-sspi/internal/client/depot_client"
	"github.com/vmware/terraform-provider-sspi/internal/client/iam_client"
	"github.com/vmware/terraform-provider-sspi/internal/client/upgrade_client"
	datasource_bundle "github.com/vmware/terraform-provider-sspi/internal/provider/datasource_bundle"
	datasource_platform "github.com/vmware/terraform-provider-sspi/internal/provider/datasource_platform"
	datasource_vsphere_provider "github.com/vmware/terraform-provider-sspi/internal/provider/datasource_vsphere_provider"
	resource_backup "github.com/vmware/terraform-provider-sspi/internal/provider/resource_backup"
	resource_backup_config "github.com/vmware/terraform-provider-sspi/internal/provider/resource_backup_config"
	resource_bundle_local "github.com/vmware/terraform-provider-sspi/internal/provider/resource_bundle_local"
	resource_ldap_identity_source "github.com/vmware/terraform-provider-sspi/internal/provider/resource_ldap_identity_source"
	resource_platform "github.com/vmware/terraform-provider-sspi/internal/provider/resource_platform"
	resource_provider "github.com/vmware/terraform-provider-sspi/internal/provider/resource_provider"
	resource_recurring_backup_config "github.com/vmware/terraform-provider-sspi/internal/provider/resource_recurring_backup_config"
	resource_restore "github.com/vmware/terraform-provider-sspi/internal/provider/resource_restore"
	resource_upgrade "github.com/vmware/terraform-provider-sspi/internal/provider/resource_upgrade"
	resource_user_password "github.com/vmware/terraform-provider-sspi/internal/provider/resource_user_password"
)

var _ provider.Provider = &SspiProvider{}
var _ provider.ProviderWithFunctions = &SspiProvider{}

// SspiProvider defines the provider implementation.
type SspiProvider struct {
	version string
}

// SspiProviderModel describes the provider data model.
type SspiProviderModel struct {
	Host     types.String `tfsdk:"host"`
	Endpoint types.String `tfsdk:"endpoint"`
	Username types.String `tfsdk:"username"`
	Password types.String `tfsdk:"password"`
	Insecure types.Bool   `tfsdk:"insecure"`
}

func New(version string) func() provider.Provider {
	return func() provider.Provider {
		return &SspiProvider{
			version: version,
		}
	}
}

func (p *SspiProvider) Metadata(ctx context.Context, req provider.MetadataRequest, resp *provider.MetadataResponse) {
	resp.TypeName = "sspi"
	resp.Version = p.version
}

func (p *SspiProvider) Schema(ctx context.Context, req provider.SchemaRequest, resp *provider.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: `The **SSPI** (Security Services Platform Installer) provider manages the SSPI appliance:
vCenter provider registration, software package management, cluster deployment and lifecycle, identity/access,
and backup/restore configuration for the SSPI appliance itself.

For Day-2 operational workflows against a deployed SSP cluster, see the companion ` + "`terraform-provider-ssp`" + ` provider.`,
		Attributes: map[string]schema.Attribute{
			"host": schema.StringAttribute{
				MarkdownDescription: "The base URL/host of the SSPI appliance. Can be specified with the `SSPI_HOST` environment variable.",
				Optional:            true,
			},
			"endpoint": schema.StringAttribute{
				MarkdownDescription: "The API endpoint of the SSPI appliance. Can be specified with the `SSPI_ENDPOINT` environment variable.",
				Optional:            true,
			},
			"username": schema.StringAttribute{
				MarkdownDescription: "The local administrator username for SSPI. Must hold the `enterprise_admin` " +
					"role — nearly every `sspi_*` write requires it (alone, or paired with `auditor`); " +
					"`auditor`/`security_op` alone can only read via `data.sspi_*`, and will get an HTTP 403 " +
					"on the first write. `Configure()` calls `GET /sspi/iam/current-user-info` and warns if this " +
					"account lacks `enterprise_admin`. Can be specified with the `SSPI_USERNAME` environment variable.",
				Optional: true,
			},
			"password": schema.StringAttribute{
				MarkdownDescription: "The local administrator password for SSPI. Can be specified with the `SSPI_PASSWORD` environment variable.",
				Optional:            true,
				Sensitive:           true,
			},
			"insecure": schema.BoolAttribute{
				MarkdownDescription: "Allow insecure TLS connections to the SSPI appliance. Can be specified with the `SSPI_INSECURE` environment variable. Defaults to false.",
				Optional:            true,
			},
		},
	}
}

func (p *SspiProvider) Configure(ctx context.Context, req provider.ConfigureRequest, resp *provider.ConfigureResponse) {
	var data SspiProviderModel

	resp.Diagnostics.Append(req.Config.Get(ctx, &data)...)
	if resp.Diagnostics.HasError() {
		return
	}

	endpoint := os.Getenv("SSPI_HOST")
	if endpoint == "" {
		endpoint = os.Getenv("SSPI_ENDPOINT")
	}
	username := os.Getenv("SSPI_USERNAME")
	password := os.Getenv("SSPI_PASSWORD")

	if !data.Host.IsNull() && !data.Host.IsUnknown() {
		endpoint = data.Host.ValueString()
	} else if !data.Endpoint.IsNull() && !data.Endpoint.IsUnknown() {
		endpoint = data.Endpoint.ValueString()
	}
	if !data.Username.IsNull() && !data.Username.IsUnknown() {
		username = data.Username.ValueString()
	}
	if !data.Password.IsNull() && !data.Password.IsUnknown() {
		password = data.Password.ValueString()
	}

	insecure := false
	if !data.Insecure.IsNull() && !data.Insecure.IsUnknown() {
		insecure = data.Insecure.ValueBool()
	} else if os.Getenv("SSPI_INSECURE") == "true" {
		insecure = true
	}

	if endpoint == "" {
		resp.Diagnostics.AddError(
			"Missing SSPI Appliance Configuration",
			"Set the provider's \"host\" attribute (or the SSPI_HOST/SSPI_ENDPOINT environment variable) "+
				"to the base URL of the SSPI appliance.",
		)
		return
	}

	if !strings.HasPrefix(endpoint, "http://") && !strings.HasPrefix(endpoint, "https://") {
		endpoint = "https://" + endpoint
	}
	endpoint = strings.TrimRight(endpoint, "/")

	transport := &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: insecure,
			MinVersion:         tls.VersionTLS12,
		},
	}
	if strings.HasPrefix(endpoint, "http://127.0.0.1") || strings.HasPrefix(endpoint, "http://localhost") {
		transport.Proxy = nil
	} else if cd, ok := proxy.FromEnvironment().(proxy.ContextDialer); ok {
		transport.DialContext = cd.DialContext
	}

	httpClient := &http.Client{
		Timeout:   5 * time.Minute,
		Transport: transport,
	}

	authEditor := func(ctx context.Context, req *http.Request) error {
		req.SetBasicAuth(username, password)
		return nil
	}

	clients := &ClientData{}

	apiClient, err := api_client.NewClientWithResponses(
		endpoint,
		api_client.WithHTTPClient(httpClient),
		api_client.WithRequestEditorFn(authEditor),
	)
	if err != nil {
		resp.Diagnostics.AddError("Invalid SSPI Appliance Configuration", fmt.Sprintf("Unable to create SSPI API client: %s", err))
		return
	}
	clients.API = apiClient

	depotClient, err := depot_client.NewClientWithResponses(
		endpoint,
		depot_client.WithHTTPClient(httpClient),
		depot_client.WithRequestEditorFn(authEditor),
	)
	if err != nil {
		resp.Diagnostics.AddError("Invalid SSPI Appliance Configuration", fmt.Sprintf("Unable to create SSPI Depot client: %s", err))
		return
	}
	clients.Depot = depotClient

	iamClient, err := iam_client.NewClientWithResponses(
		endpoint,
		iam_client.WithHTTPClient(httpClient),
		iam_client.WithRequestEditorFn(authEditor),
	)
	if err != nil {
		resp.Diagnostics.AddError("Invalid SSPI Appliance Configuration", fmt.Sprintf("Unable to create SSPI IAM client: %s", err))
		return
	}
	clients.IAM = iamClient

	verifyEnterpriseAdminRole(ctx, iamClient, username, &resp.Diagnostics)

	upgradeClient, err := upgrade_client.NewClientWithResponses(
		endpoint,
		upgrade_client.WithHTTPClient(httpClient),
		upgrade_client.WithRequestEditorFn(authEditor),
	)
	if err != nil {
		resp.Diagnostics.AddError("Invalid SSPI Appliance Configuration", fmt.Sprintf("Unable to create SSPI Upgrade client: %s", err))
		return
	}
	clients.Upgrade = upgradeClient

	resp.DataSourceData = clients
	resp.ResourceData = clients
}

func (p *SspiProvider) Resources(ctx context.Context) []func() resource.Resource {
	return []func() resource.Resource{
		resource_provider.NewProviderResource,
		resource_platform.NewPlatformResource,
		resource_bundle_local.NewBundleLocalResource,
		resource_ldap_identity_source.NewLdapIdentitySourceResource,
		resource_backup_config.NewBackupConfigResource,
		resource_recurring_backup_config.NewRecurringBackupConfigResource,
		resource_user_password.NewUserPasswordResource,
		resource_upgrade.NewUpgradeResource,
		resource_backup.NewBackupResource,
		resource_restore.NewRestoreResource,
	}
}

func (p *SspiProvider) DataSources(ctx context.Context) []func() datasource.DataSource {
	return []func() datasource.DataSource{
		datasource_vsphere_provider.NewVsphereProviderDataSource,
		datasource_platform.NewPlatformDataSource,
		datasource_bundle.NewBundleDataSource,
	}
}

func (p *SspiProvider) Functions(ctx context.Context) []func() function.Function {
	return []func() function.Function{}
}

// verifyEnterpriseAdminRole calls GET /sspi/iam/current-user-info and warns if the
// configured account's roles don't include enterprise_admin, the role nearly every
// sspi_* write operation requires (per sspi_public_apis.yaml's per-operation
// security: blocks). This surfaces a role mismatch as an explicit diagnostic at
// terraform plan time rather than as an unexplained 403 on the first resource
// write. It is a warning, not an error, because an auditor-only account is a
// legitimate configuration for a root module that only reads via data.sspi_*
// data sources.
func verifyEnterpriseAdminRole(ctx context.Context, client *iam_client.ClientWithResponses, username string, diags *diag.Diagnostics) {
	userInfo, err := client.GetCurrentUserInfoWithResponse(ctx)
	if err != nil {
		diags.AddWarning(
			"Unable to Verify SSPI Account Role",
			fmt.Sprintf("Could not call GET /sspi/iam/current-user-info to pre-verify the configured account's "+
				"role: %s. Nearly all sspi_* resources require the enterprise_admin role; an account without it "+
				"will fail with an HTTP 403 on its first write operation.", err),
		)
		return
	}
	if userInfo.JSON200 == nil {
		return
	}

	var roleNames []string
	hasEnterpriseAdmin := false
	if userInfo.JSON200.Roles != nil {
		for _, role := range *userInfo.JSON200.Roles {
			roleNames = append(roleNames, string(role.Role))
			if role.Role == iam_client.ENTERPRISEADMIN {
				hasEnterpriseAdmin = true
			}
		}
	}
	if hasEnterpriseAdmin {
		return
	}

	roles := "none"
	if len(roleNames) > 0 {
		roles = strings.Join(roleNames, ", ")
	}
	diags.AddWarning(
		"SSPI Account Missing enterprise_admin Role",
		fmt.Sprintf("The configured SSPI account %q has role(s) %s, not enterprise_admin. Nearly all sspi_* "+
			"resources require enterprise_admin (alone, or paired with auditor) for write operations; auditor "+
			"alone is only sufficient for data.sspi_* data sources. Expect HTTP 403 errors on any sspi_* "+
			"resource Create/Update/Delete unless the account is reconfigured with enterprise_admin.",
			username, roles),
	)
}
