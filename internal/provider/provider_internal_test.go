// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.

package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/diag"

	"github.com/vmware/terraform-provider-sspi/internal/client/iam_client"
)

func newCurrentUserInfoMockServer(t *testing.T, roles []string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /sspi/iam/current-user-info", func(w http.ResponseWriter, r *http.Request) {
		roleObjs := make([]map[string]any, 0, len(roles))
		for _, role := range roles {
			roleObjs = append(roleObjs, map[string]any{"role": role})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"name":  "tfuser",
			"roles": roleObjs,
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestVerifyEnterpriseAdminRole_MissingRole(t *testing.T) {
	srv := newCurrentUserInfoMockServer(t, []string{"AUDITOR"})
	client, err := iam_client.NewClientWithResponses(srv.URL)
	if err != nil {
		t.Fatalf("failed to create iam client: %s", err)
	}

	var diags diag.Diagnostics
	verifyEnterpriseAdminRole(context.Background(), client, "auditor-user", &diags)

	if !diags.HasError() && !hasWarning(diags) {
		t.Fatalf("expected a warning diagnostic for an account missing enterprise_admin, got none: %v", diags)
	}
	if diags.HasError() {
		t.Fatalf("expected a warning, not an error, for a role mismatch: %v", diags)
	}
}

func TestVerifyEnterpriseAdminRole_HasRole(t *testing.T) {
	srv := newCurrentUserInfoMockServer(t, []string{"ENTERPRISE_ADMIN"})
	client, err := iam_client.NewClientWithResponses(srv.URL)
	if err != nil {
		t.Fatalf("failed to create iam client: %s", err)
	}

	var diags diag.Diagnostics
	verifyEnterpriseAdminRole(context.Background(), client, "admin-user", &diags)

	if len(diags) != 0 {
		t.Fatalf("expected no diagnostics for an enterprise_admin account, got: %v", diags)
	}
}

// TestVerifyEnterpriseAdminRole_NetworkError verifies the third branch: if
// GET /sspi/iam/current-user-info itself fails (e.g. the appliance is
// briefly unreachable), verifyEnterpriseAdminRole emits an informational
// warning rather than propagating the error as a hard Configure() failure —
// this is a best-effort pre-check, not a required connectivity gate.
func TestVerifyEnterpriseAdminRole_NetworkError(t *testing.T) {
	// Port 1 is a reserved, always-closed port: connection attempts fail
	// immediately with "connection refused" rather than timing out.
	client, err := iam_client.NewClientWithResponses("http://127.0.0.1:1")
	if err != nil {
		t.Fatalf("failed to create iam client: %s", err)
	}

	var diags diag.Diagnostics
	verifyEnterpriseAdminRole(context.Background(), client, "some-user", &diags)

	if diags.HasError() {
		t.Fatalf("expected a warning, not an error, for a connectivity failure: %v", diags)
	}
	if !hasWarning(diags) {
		t.Fatalf("expected a warning diagnostic when the current-user-info call fails, got none: %v", diags)
	}
}

func hasWarning(diags diag.Diagnostics) bool {
	for _, d := range diags {
		if d.Severity() == diag.SeverityWarning {
			return true
		}
	}
	return false
}
