package hyperbolic

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/anthonylu23/switchboard-cli/internal/credentials"
)

func TestRealClientSendsBearerAuthAndDecodesLifecycleEndpoints(t *testing.T) {
	store := credentials.NewMemoryStore()
	if err := store.Set("hyperbolic/api_key", "test-api-key"); err != nil {
		t.Fatalf("Set returned error: %v", err)
	}
	var sawAuth bool
	var sawRent bool
	var sawTerminate bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer test-api-key" {
			sawAuth = true
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/billing/get_current_balance":
			_, _ = w.Write([]byte(`{"credits":100}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v2/marketplace/virtual-machine-options":
			_, _ = w.Write([]byte(`[{"gpuCount":1,"costPerHour":2.5},{"gpuCount":4,"costPerHour":9.5}]`))
		case r.Method == http.MethodPost && r.URL.Path == "/v2/marketplace/virtual-machine-rentals":
			var req VirtualMachineRentalRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode rental request: %v", err)
			}
			if req.ConfigID != "vm-config-test" || req.GPUCount != 2 {
				t.Fatalf("rental request = %#v", req)
			}
			sawRent = true
			_, _ = w.Write([]byte(`{"id":123,"externalId":"rental-external","costPerHour":500,"status":"starting","meta":{"public_ip":"203.0.113.1","username":"ubuntu"}}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v2/marketplace/virtual-machine-rentals":
			_, _ = w.Write([]byte(`[{"id":123,"externalId":"rental-external","costPerHour":500,"status":"running","meta":{"public_ip":"203.0.113.1","username":"ubuntu"}}]`))
		case r.Method == http.MethodPost && r.URL.Path == "/v2/marketplace/virtual-machine-rentals/terminate":
			var req struct {
				RentalID int `json:"rentalId"`
			}
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode terminate request: %v", err)
			}
			if req.RentalID != 123 {
				t.Fatalf("terminate request = %#v", req)
			}
			sawTerminate = true
			_, _ = w.Write([]byte(`{"ok":true}`))
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
	options, err := client.ListVirtualMachineOptions(context.Background())
	if err != nil || len(options) != 2 || options[1].GPUCount != 4 {
		t.Fatalf("options=%#v err=%v", options, err)
	}
	rental, err := client.RentVirtualMachine(context.Background(), VirtualMachineRentalRequest{ConfigID: "vm-config-test", GPUCount: 2})
	if err != nil || rental.RefID() != "123" {
		t.Fatalf("rental=%#v err=%v", rental, err)
	}
	instance, err := client.GetVirtualMachineInstance(context.Background(), "123")
	if err != nil || instance.PublicIP() != "203.0.113.1" {
		t.Fatalf("instance=%#v err=%v", instance, err)
	}
	if err := client.TerminateVirtualMachine(context.Background(), "123"); err != nil {
		t.Fatalf("TerminateVirtualMachine returned error: %v", err)
	}
	if !sawAuth || !sawRent || !sawTerminate {
		t.Fatalf("sawAuth=%v sawRent=%v sawTerminate=%v", sawAuth, sawRent, sawTerminate)
	}
}

func TestRealClientUsesEnvCredentialWhenStoreUnset(t *testing.T) {
	t.Setenv("HYPERBOLIC_API_KEY", "env-api-key")
	var authHeader string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeader = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"credits":100}`))
	}))
	defer server.Close()

	client, err := newRealClient(Config{BaseURL: server.URL, APITimeoutSeconds: 2})
	if err != nil {
		t.Fatalf("newRealClient returned error: %v", err)
	}
	if err := client.ValidateAuth(context.Background()); err != nil {
		t.Fatalf("ValidateAuth returned error: %v", err)
	}
	if authHeader != "Bearer env-api-key" {
		t.Fatalf("authorization = %q", authHeader)
	}
}

func TestRealClientDecodesAPIError(t *testing.T) {
	store := credentials.NewMemoryStore()
	if err := store.Set("hyperbolic/api_key", "test-api-key"); err != nil {
		t.Fatalf("Set returned error: %v", err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"code":"invalid-api-key","message":"bad key"}}`))
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
	if apiErr.StatusCode != http.StatusUnauthorized || apiErr.Code != "invalid-api-key" {
		t.Fatalf("apiErr = %#v", apiErr)
	}
}
