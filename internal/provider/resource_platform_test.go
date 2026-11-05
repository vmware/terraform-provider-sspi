// © Broadcom. All Rights Reserved.
// The term "Broadcom" refers to Broadcom Inc. and/or its subsidiaries.

package provider_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/hashicorp/terraform-plugin-testing/helper/resource"
	"github.com/hashicorp/terraform-plugin-testing/terraform"
)

// platformMockAPI simulates the subset of the SSPI appliance platform-LCM
// API (POST/GET/PUT/DELETE /sspi/platforms[/{id}], GET
// /sspi/platforms/{id}/status) needed to drive ssp_platform through a full
// deploy/read/teardown cycle without a live SSPI appliance.
//
// Create/Update bodies are stored and echoed back verbatim on GET (with "id"
// injected), so every Optional+Computed attribute the resource round-trips
// stays consistent between apply and the post-apply plan-convergence check.
type platformMockAPI struct {
	mu     sync.Mutex
	exists bool
	body   map[string]any
	// desiredStates records the desired_state of every POST/PUT, in order.
	desiredStates []string
}

func (m *platformMockAPI) recordDesiredState(body map[string]any) {
	ds, _ := body["desired_state"].(string)
	m.desiredStates = append(m.desiredStates, ds)
}

func (m *platformMockAPI) lastDesiredState() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.desiredStates) == 0 {
		return ""
	}
	return m.desiredStates[len(m.desiredStates)-1]
}

func newPlatformMockServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv, _ := newPlatformMockServerWithAPI(t)
	return srv
}

func newPlatformMockServerWithAPI(t *testing.T) (*httptest.Server, *platformMockAPI) {
	t.Helper()
	m := &platformMockAPI{}

	writeJSON := func(w http.ResponseWriter, status int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(v)
	}

	readBody := func(r *http.Request) map[string]any {
		raw, _ := io.ReadAll(r.Body)
		var out map[string]any
		_ = json.Unmarshal(raw, &out)
		return out
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /sspi/platforms", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.exists = true
		m.body = readBody(r)
		m.body["_revision"] = 0
		m.recordDesiredState(m.body)
		writeJSON(w, http.StatusAccepted, map[string]any{"id": "platform-1"})
	})
	mux.HandleFunc("GET /sspi/platforms/{id}", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		if !m.exists {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		resp := map[string]any{}
		for k, v := range m.body {
			resp[k] = v
		}
		resp["id"] = "platform-1"
		fillServerDefaults(resp)
		writeJSON(w, http.StatusOK, resp)
	})
	mux.HandleFunc("PUT /sspi/platforms/{id}", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		updated := readBody(r)
		if rev, ok := updated["_revision"].(float64); ok {
			updated["_revision"] = rev + 1
		} else {
			updated["_revision"] = 1
		}
		m.body = updated
		m.recordDesiredState(updated)
		writeJSON(w, http.StatusAccepted, map[string]any{"id": "platform-1"})
	})
	mux.HandleFunc("DELETE /sspi/platforms/{id}", func(w http.ResponseWriter, r *http.Request) {
		m.mu.Lock()
		defer m.mu.Unlock()
		m.exists = false
		writeJSON(w, http.StatusAccepted, map[string]any{"id": "platform-1"})
	})
	mux.HandleFunc("GET /sspi/platforms/{id}/status", func(w http.ResponseWriter, r *http.Request) {
		if m.lastDesiredState() == "PRECHECK_ONLY" {
			// A precheck-only run completes the precheck workflow and leaves the
			// deployment workflow listed but not started.
			writeJSON(w, http.StatusOK, map[string]any{
				"phase": "PRECHECK",
				"workflow_results": []map[string]any{
					{"display_name": "Precheck Workflow", "type": "PLATFORM_DEPLOYMENT_PRECHECK", "state": "COMPLETED"},
					{"display_name": "Deployment Workflow", "type": "PLATFORM_DEPLOYMENT", "state": "NOT_STARTED"},
				},
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"phase": "DEPLOYMENT",
			"workflow_results": []map[string]any{
				{"display_name": "Deploy", "state": "COMPLETED"},
			},
		})
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, m
}

// fillServerDefaults mimics the real appliance, which resolves and returns
// names and defaults the client never sent (e.g. vm_folder_name,
// cluster_name, addon_ids, preserve_addons) once a platform is deployed. The
// mock otherwise echoes the request verbatim, which hid the "Provider
// produced inconsistent result after apply" bug for Optional attributes that
// were not also Computed.
func fillServerDefaults(resp map[string]any) {
	sub := func(key string) map[string]any {
		m, _ := resp[key].(map[string]any)
		if m == nil {
			m = map[string]any{}
			resp[key] = m
		}
		return m
	}
	setDefault := func(m map[string]any, k string, v any) {
		if _, ok := m[k]; !ok {
			m[k] = v
		}
	}

	compute := sub("compute")
	setDefault(compute, "datacenter_name", "Datacenter-0")
	setDefault(compute, "cluster_name", "Tenant-Cluster-0")
	setDefault(compute, "content_datastore_name", "vsanDatastore")
	setDefault(compute, "storage_policy_name", "vSAN Default Storage Policy")
	setDefault(compute, "vm_folder_name", "tf-unit-test-platform")
	setDefault(compute, "vm_template", "tf-unit-test-platform-ssp-node")
	setDefault(compute, "enable_resource_reservation", false)

	service := sub("service")
	setDefault(service, "preserve_addons", false)
	setDefault(service, "addon_ids", []any{"addon-1", "addon-2"})
	setDefault(service, "ssp_bundle_version", "5.2.0.1")

	if network := sub("network"); network != nil {
		if ncs, ok := network["network_configs"].([]any); ok {
			for _, nc := range ncs {
				if m, ok := nc.(map[string]any); ok {
					setDefault(m, "network_name", "vsx-network")
					setDefault(m, "portgroup_name", "vsx-portgroup")
				}
			}
		}
	}
}

func testUnitPlatformConfig(host string) string {
	return testUnitPlatformConfigWithWorkerCount(host, 3)
}

func testUnitPlatformConfigWithWorkerCount(host string, workerCount int) string {
	return testUnitSSPIProviderConfig(host) + fmt.Sprintf(`
resource "sspi_platform" "test" {
  provider_id              = "vsphere-provider-1"
  display_name             = "tf-unit-test-platform"
  form_factor              = "SMALL"
  ssp_type                 = "ATP"
  worker_count             = %d
  controller_count         = 3
  datacenter_id             = "datacenter-1"
  cluster_id                = "cluster-1"
  content_datastore_id      = "datastore-1"
  domain                    = "corp.local"
  dns_servers               = ["10.0.0.1"]
  ntp_server                = "ntp.corp.local"
  network_id                = "network-1"
  portgroup_id              = "portgroup-1"
  platform_default_gateway  = "10.0.0.1"
  platform_subnet           = "10.0.0.0/24"
  node_ip_pool              = ["10.0.0.10-10.0.0.20"]
  service_ip_pool           = ["10.0.0.30-10.0.0.40"]
  ingress_fqdn              = "ssp.corp.local"
  kafka_fqdn                = "kafka.corp.local"
  ssp_bundle_id             = "bundle-1"
}
`, workerCount)
}

// TestUnitPlatformResource exercises ssp_platform's Create, Read, Update
// (worker_count change, exercising the _revision-aware PUT), and Delete
// against a mocked SSPI appliance API.
func TestUnitPlatformResource(t *testing.T) {
	srv := newPlatformMockServer(t)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: testUnitPlatformConfig(srv.URL),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("sspi_platform.test", "id", "platform-1"),
					resource.TestCheckResourceAttr("sspi_platform.test", "provider_id", "vsphere-provider-1"),
					resource.TestCheckResourceAttr("sspi_platform.test", "form_factor", "SMALL"),
					resource.TestCheckResourceAttr("sspi_platform.test", "worker_count", "3"),
					resource.TestCheckResourceAttr("sspi_platform.test", "status", "DEPLOYMENT"),
					// Optional attributes left unset in config but filled in by
					// the appliance must land in state as computed values.
					resource.TestCheckResourceAttr("sspi_platform.test", "vm_folder_name", "tf-unit-test-platform"),
					resource.TestCheckResourceAttr("sspi_platform.test", "cluster_name", "Tenant-Cluster-0"),
					resource.TestCheckResourceAttr("sspi_platform.test", "datastore", "vsanDatastore"),
					resource.TestCheckResourceAttr("sspi_platform.test", "storage_policy_name", "vSAN Default Storage Policy"),
					resource.TestCheckResourceAttr("sspi_platform.test", "network", "vsx-network"),
					resource.TestCheckResourceAttr("sspi_platform.test", "preserve_addons", "false"),
					resource.TestCheckResourceAttr("sspi_platform.test", "addon_ids.#", "2"),
				),
			},
			// Re-planning the same config must be empty: the computed values
			// must not show up as drift.
			{
				Config:   testUnitPlatformConfig(srv.URL),
				PlanOnly: true,
			},
			// Update: change worker_count in place; requires the mock's PUT
			// handler to accept the _revision the resource fetched via GET
			// beforehand.
			{
				Config: testUnitPlatformConfigWithWorkerCount(srv.URL, 5),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("sspi_platform.test", "id", "platform-1"),
					resource.TestCheckResourceAttr("sspi_platform.test", "worker_count", "5"),
				),
			},
		},
	})
}

// TestUnitPlatformResource_PrecheckOnly runs only the precheck
// (desired_state=PRECHECK_ONLY), then flips precheck_only off to start the saved
// deployment (CONTINUE).
func TestUnitPlatformResource_PrecheckOnly(t *testing.T) {
	srv, m := newPlatformMockServerWithAPI(t)

	precheckConfig := strings.Replace(testUnitPlatformConfig(srv.URL),
		`ssp_bundle_id             = "bundle-1"`,
		`ssp_bundle_id             = "bundle-1"
  precheck_only             = true`, 1)

	resource.UnitTest(t, resource.TestCase{
		ProtoV6ProviderFactories: testAccProtoV6ProviderFactories,
		Steps: []resource.TestStep{
			{
				Config: precheckConfig,
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckResourceAttr("sspi_platform.test", "id", "platform-1"),
					resource.TestCheckResourceAttr("sspi_platform.test", "precheck_only", "true"),
					func(*terraform.State) error {
						if got := m.lastDesiredState(); got != "PRECHECK_ONLY" {
							return fmt.Errorf("desired_state after create = %q, want PRECHECK_ONLY", got)
						}
						return nil
					},
				),
			},
			{
				Config:   precheckConfig,
				PlanOnly: true,
			},
			{
				Config: testUnitPlatformConfig(srv.URL),
				Check: resource.ComposeAggregateTestCheckFunc(
					resource.TestCheckNoResourceAttr("sspi_platform.test", "precheck_only"),
					func(*terraform.State) error {
						if got := m.lastDesiredState(); got != "CONTINUE" {
							return fmt.Errorf("desired_state after precheck_only removed = %q, want CONTINUE", got)
						}
						return nil
					},
				),
			},
		},
	})
}
