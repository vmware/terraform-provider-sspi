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

	"github.com/vmware/terraform-provider-sspi/internal/client/iam_client"
	"github.com/vmware/terraform-provider-sspi/internal/provider/pagination"
)

var _ datasource.DataSource = &LdapIdentitySourcesDataSource{}

func NewLdapIdentitySourcesDataSource() datasource.DataSource {
	return &LdapIdentitySourcesDataSource{}
}

type LdapIdentitySourcesDataSource struct {
	client *iam_client.ClientWithResponses
}

type LdapIdentitySourceSummaryModel struct {
	ID                    types.String `tfsdk:"id"`
	DomainName            types.String `tfsdk:"domain_name"`
	LdapType              types.String `tfsdk:"ldap_type"`
	BaseDistinguishedName types.String `tfsdk:"base_distinguished_name"`
}

type LdapIdentitySourcesDataSourceModel struct {
	Results []LdapIdentitySourceSummaryModel `tfsdk:"results"`
}

func (d *LdapIdentitySourcesDataSource) Metadata(ctx context.Context, req datasource.MetadataRequest, resp *datasource.MetadataResponse) {
	resp.TypeName = req.ProviderTypeName + "_ldap_identity_sources"
}

func (d *LdapIdentitySourcesDataSource) Schema(ctx context.Context, req datasource.SchemaRequest, resp *datasource.SchemaResponse) {
	resp.Schema = schema.Schema{
		MarkdownDescription: "Lists every LDAP identity source configured on the SSPI appliance.\n\n" +
			"Corresponds to `GET /sspi/iam/ldap-identity-sources`, walking every page of results.",
		Attributes: map[string]schema.Attribute{
			"results": schema.ListNestedAttribute{
				MarkdownDescription: "The LDAP identity sources configured on the SSPI appliance.",
				Computed:            true,
				NestedObject: schema.NestedAttributeObject{
					Attributes: map[string]schema.Attribute{
						"id":                      schema.StringAttribute{Computed: true, MarkdownDescription: "Unique identifier of the LDAP identity source."},
						"domain_name":             schema.StringAttribute{Computed: true, MarkdownDescription: "Authentication domain suffix routed to this identity source."},
						"ldap_type":               schema.StringAttribute{Computed: true, MarkdownDescription: "LDAP server implementation (`ACTIVE_DIRECTORY` or `OPEN_LDAP`)."},
						"base_distinguished_name": schema.StringAttribute{Computed: true, MarkdownDescription: "Base DN searched for users and groups."},
					},
				},
			},
		},
	}
}

func (d *LdapIdentitySourcesDataSource) Configure(ctx context.Context, req datasource.ConfigureRequest, resp *datasource.ConfigureResponse) {
	if req.ProviderData == nil {
		return
	}

	clientProvider, ok := req.ProviderData.(interface {
		GetIAM() *iam_client.ClientWithResponses
	})

	if !ok {
		resp.Diagnostics.AddError(
			"Unexpected Data Source Configure Type",
			fmt.Sprintf("Expected provider data with GetIAM(), got: %T.", req.ProviderData),
		)
		return
	}

	d.client = clientProvider.GetIAM()
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

func (d *LdapIdentitySourcesDataSource) Read(ctx context.Context, req datasource.ReadRequest, resp *datasource.ReadResponse) {
	var data LdapIdentitySourcesDataSourceModel

	tflog.Debug(ctx, "Reading SSPI LDAP Identity Sources Data Source")

	sources, err := pagination.FetchAll(func(offset int) ([]iam_client.LdapIdentitySource, int, error) {
		o := offset
		readResp, err := d.client.GetAllLdapIdentitySourcesWithResponse(ctx, &iam_client.GetAllLdapIdentitySourcesParams{Offset: &o})
		if err != nil {
			return nil, 0, err
		}
		if readResp.StatusCode() != http.StatusOK || readResp.JSON200 == nil {
			return nil, 0, fmt.Errorf("unexpected response status code: %d", readResp.StatusCode())
		}
		var page []iam_client.LdapIdentitySource
		if readResp.JSON200.Results != nil {
			page = *readResp.JSON200.Results
		}
		total := 0
		if readResp.JSON200.TotalResultCount != nil {
			total = int(*readResp.JSON200.TotalResultCount)
		}
		return page, total, nil
	})
	if err != nil {
		resp.Diagnostics.AddError("Error Reading SSPI LDAP Identity Sources Data Source", "Could not list LDAP identity sources: "+err.Error())
		return
	}

	for _, src := range sources {
		item := LdapIdentitySourceSummaryModel{
			DomainName:            types.StringValue(src.DomainName),
			LdapType:              types.StringValue(string(src.LdapType)),
			BaseDistinguishedName: types.StringValue(src.BaseDistinguishedName),
		}
		if src.Id != nil {
			item.ID = types.StringValue(*src.Id)
		}
		data.Results = append(data.Results, item)
	}

	resp.Diagnostics.Append(resp.State.Set(ctx, &data)...)
}
