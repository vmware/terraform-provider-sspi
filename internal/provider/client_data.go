// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.

package provider

import (
	"github.com/vmware/terraform-provider-sspi/internal/client/api_client"
	"github.com/vmware/terraform-provider-sspi/internal/client/depot_client"
	"github.com/vmware/terraform-provider-sspi/internal/client/iam_client"
	"github.com/vmware/terraform-provider-sspi/internal/client/upgrade_client"
)

// ClientData is the shared provider configuration passed to resources and data sources.
type ClientData struct {
	API     *api_client.ClientWithResponses
	Depot   *depot_client.ClientWithResponses
	IAM     *iam_client.ClientWithResponses
	Upgrade *upgrade_client.ClientWithResponses
}

func (c *ClientData) GetAPI() *api_client.ClientWithResponses         { return c.API }
func (c *ClientData) GetDepot() *depot_client.ClientWithResponses     { return c.Depot }
func (c *ClientData) GetIAM() *iam_client.ClientWithResponses         { return c.IAM }
func (c *ClientData) GetUpgrade() *upgrade_client.ClientWithResponses { return c.Upgrade }
