package lambda

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anthonylu23/switchboard-cli/internal/credentials"
)

func TestRealClientSendsBearerAuthAndDecodesLifecycleEndpoints(t *testing.T) {
	store := credentials.NewMemoryStore()
	if err := store.Set("lambda/api_key", "test-api-key"); err != nil {
		t.Fatalf("Set returned error: %v", err)
	}
	var sawAuth bool
	var sawLaunch bool
	var sawTerminate bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer test-api-key" {
			sawAuth = true
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/instances":
			_, _ = w.Write([]byte(`{"data":[{"id":"i-1","status":"active","ssh_key_names":[],"file_system_names":[],"region":{"name":"us-west-1"},"instance_type":{"name":"gpu_1x_a10","gpu_description":"A10 (24 GB)","price_cents_per_hour":75,"specs":{"vcpus":30,"memory_gib":200,"storage_gib":1400,"gpus":1}}}]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/instance-types":
			_, _ = w.Write([]byte(`{"data":{"gpu_1x_a10":{"instance_type":{"name":"gpu_1x_a10","description":"A10","gpu_description":"A10 (24 GB)","price_cents_per_hour":75,"specs":{"vcpus":30,"memory_gib":200,"storage_gib":1400,"gpus":1}},"regions_with_capacity_available":[{"name":"us-west-1","description":"California"}]}}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/api/v1/instances/i-1":
			_, _ = w.Write([]byte(`{"data":{"id":"i-1","ip":"203.0.113.1","status":"active","ssh_key_names":[],"file_system_names":[],"region":{"name":"us-west-1"},"instance_type":{"name":"gpu_1x_a10","gpu_description":"A10 (24 GB)","price_cents_per_hour":75,"specs":{"vcpus":30,"memory_gib":200,"storage_gib":1400,"gpus":1}}}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/instance-operations/launch":
			var req LaunchInstanceRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode launch request: %v", err)
			}
			if req.RegionName != "us-west-1" || req.InstanceTypeName != "gpu_1x_a10" || !strings.Contains(req.UserData, "docker") {
				t.Fatalf("launch request = %#v", req)
			}
			sawLaunch = true
			_, _ = w.Write([]byte(`{"data":{"instance_ids":["i-1"]}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/api/v1/instance-operations/terminate":
			sawTerminate = true
			_, _ = w.Write([]byte(`{"data":{}}`))
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	client, err := newRealClient(Config{BaseURL: server.URL, APITimeoutSeconds: 2, Credentials: credentials.Resolver{Store: store}})
	if err != nil {
		t.Fatalf("newRealClient returned error: %v", err)
	}
	if err := client.ValidateAuth(context.Background()); err != nil {
		t.Fatalf("ValidateAuth returned error: %v", err)
	}
	if _, err := client.ListInstanceTypes(context.Background()); err != nil {
		t.Fatalf("ListInstanceTypes returned error: %v", err)
	}
	if _, err := client.GetInstance(context.Background(), "i-1"); err != nil {
		t.Fatalf("GetInstance returned error: %v", err)
	}
	if ids, err := client.LaunchInstance(context.Background(), LaunchInstanceRequest{RegionName: "us-west-1", InstanceTypeName: "gpu_1x_a10", SSHKeyNames: []string{"switchboard"}, UserData: "docker"}); err != nil || len(ids) != 1 || ids[0] != "i-1" {
		t.Fatalf("LaunchInstance ids=%#v err=%v", ids, err)
	}
	if err := client.TerminateInstances(context.Background(), []string{"i-1"}); err != nil {
		t.Fatalf("TerminateInstances returned error: %v", err)
	}
	if !sawAuth || !sawLaunch || !sawTerminate {
		t.Fatalf("sawAuth=%v sawLaunch=%v sawTerminate=%v", sawAuth, sawLaunch, sawTerminate)
	}
}

func TestRealClientDecodesAPIError(t *testing.T) {
	store := credentials.NewMemoryStore()
	if err := store.Set("lambda/api_key", "test-api-key"); err != nil {
		t.Fatalf("Set returned error: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"code":"global/invalid-api-key","message":"bad key","suggestion":"fix key"}}`))
	}))
	defer server.Close()
	client, err := newRealClient(Config{BaseURL: server.URL, APITimeoutSeconds: 2, Credentials: credentials.Resolver{Store: store}})
	if err != nil {
		t.Fatalf("newRealClient returned error: %v", err)
	}
	err = client.ValidateAuth(context.Background())
	apiErr, ok := err.(*apiError)
	if !ok {
		t.Fatalf("err = %#v, want apiError", err)
	}
	if apiErr.StatusCode != http.StatusUnauthorized || apiErr.Code != "global/invalid-api-key" {
		t.Fatalf("apiErr = %#v", apiErr)
	}
}

func TestRealClientUsesStoredCredentialWhenEnvUnset(t *testing.T) {
	store := credentials.NewMemoryStore()
	if err := store.Set("lambda/api_key", "stored-api-key"); err != nil {
		t.Fatalf("Set returned error: %v", err)
	}
	var authHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()
	client, err := newRealClient(Config{
		BaseURL:           server.URL,
		APITimeoutSeconds: 2,
		Credentials:       credentials.Resolver{Store: store},
	})
	if err != nil {
		t.Fatalf("newRealClient returned error: %v", err)
	}
	if err := client.ValidateAuth(context.Background()); err != nil {
		t.Fatalf("ValidateAuth returned error: %v", err)
	}
	if authHeader != "Bearer stored-api-key" {
		t.Fatalf("authorization = %q", authHeader)
	}
}
