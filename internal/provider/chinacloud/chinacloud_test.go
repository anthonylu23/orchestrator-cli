package chinacloud

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/anthonylu23/switchboard-cli/internal/app"
)

func TestDefinitionsContainTopFiveChinaCloudProviders(t *testing.T) {
	got := Names()
	want := []string{"alibaba-cloud", "baidu-ai-cloud", "huawei-cloud", "tencent-cloud", "tianyi-cloud"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("names = %#v, want %#v", got, want)
	}
	for _, def := range Definitions() {
		if def.Endpoint == "" || def.DisplayName == "" || len(def.Requirements) == 0 {
			t.Fatalf("incomplete definition: %#v", def)
		}
	}
}

func TestValidateAuthRequiresConfiguredCredentials(t *testing.T) {
	t.Setenv("ALIBABA_CLOUD_ACCESS_KEY_ID", "")
	t.Setenv("ALIBABA_CLOUD_ACCESS_KEY_SECRET", "")
	provider := New(Definitions()[0])
	err := provider.ValidateAuth(context.Background())
	if err == nil {
		t.Fatal("expected missing credential error")
	}
	if app.ProviderErrorKindOf(err) != app.ProviderErrorAuth {
		t.Fatalf("kind = %s, err = %v", app.ProviderErrorKindOf(err), err)
	}
	if strings.Contains(err.Error(), "secret-value") {
		t.Fatalf("error leaked secret: %v", err)
	}
}

func TestValidateAuthProbesEndpointWhenCredentialsExist(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodHead {
			t.Fatalf("method = %s, want HEAD", r.Method)
		}
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	def := Definition{
		Name:        "test-cloud",
		DisplayName: "Test Cloud",
		Endpoint:    server.URL,
		Requirements: []EnvRequirement{
			{Label: "ak", Names: []string{"TEST_CHINA_CLOUD_AK"}},
			{Label: "sk", Names: []string{"TEST_CHINA_CLOUD_SK"}},
		},
	}
	t.Setenv("TEST_CHINA_CLOUD_AK", "ak-value")
	t.Setenv("TEST_CHINA_CLOUD_SK", "secret-value")
	provider := New(def)
	report, err := provider.ValidateConnection(context.Background(), ConnectionOptions{})
	if err != nil {
		t.Fatalf("ValidateAuth returned error: %v", err)
	}
	if report.Mode != "endpoint_probe" || report.Authenticated || report.Endpoint != server.URL || len(report.Warnings) == 0 {
		t.Fatalf("report = %#v", report)
	}
}

func TestValidateConnectionStrictAuthRequiresAuthCommand(t *testing.T) {
	def := Definition{
		Name:           "test-cloud",
		DisplayName:    "Test Cloud",
		Endpoint:       "https://example.invalid/",
		AuthCommandEnv: "TEST_CHINA_CLOUD_AUTH_COMMAND",
		Requirements: []EnvRequirement{
			{Label: "ak", Names: []string{"TEST_CHINA_CLOUD_AK"}},
			{Label: "sk", Names: []string{"TEST_CHINA_CLOUD_SK"}},
		},
	}
	t.Setenv("TEST_CHINA_CLOUD_AK", "ak-value")
	t.Setenv("TEST_CHINA_CLOUD_SK", "secret-value")
	provider := New(def)
	report, err := provider.ValidateConnection(context.Background(), ConnectionOptions{RequireAuthenticated: true})
	if err == nil {
		t.Fatal("expected strict auth failure")
	}
	if app.ProviderErrorKindOf(err) != app.ProviderErrorAuth {
		t.Fatalf("kind = %s, err = %v", app.ProviderErrorKindOf(err), err)
	}
	if report.Authenticated || report.AuthCommandEnv != "TEST_CHINA_CLOUD_AUTH_COMMAND" {
		t.Fatalf("report = %#v", report)
	}
}

func TestValidateAuthUsesAuthCommandWhenConfigured(t *testing.T) {
	def := Definition{
		Name:           "test-cloud",
		DisplayName:    "Test Cloud",
		AuthCommandEnv: "TEST_CHINA_CLOUD_AUTH_COMMAND",
		Requirements: []EnvRequirement{
			{Label: "ak", Names: []string{"TEST_CHINA_CLOUD_AK"}},
			{Label: "sk", Names: []string{"TEST_CHINA_CLOUD_SK"}},
		},
	}
	t.Setenv("TEST_CHINA_CLOUD_AUTH_COMMAND", "exit 0")
	provider := New(def)
	report, err := provider.ValidateConnection(context.Background(), ConnectionOptions{RequireAuthenticated: true})
	if err != nil {
		t.Fatalf("ValidateAuth returned error: %v", err)
	}
	if report.Mode != "auth_command" || !report.Authenticated {
		t.Fatalf("report = %#v", report)
	}

	t.Setenv("TEST_CHINA_CLOUD_AUTH_COMMAND", "exit 7")
	err = provider.ValidateAuth(context.Background())
	if err == nil {
		t.Fatal("expected auth command failure")
	}
	if app.ProviderErrorKindOf(err) != app.ProviderErrorAuth {
		t.Fatalf("kind = %s, err = %v", app.ProviderErrorKindOf(err), err)
	}
}

func TestValidateJobRejectsSubmission(t *testing.T) {
	provider := New(Definitions()[0])
	report := provider.ValidateJob(context.Background(), app.JobSpec{Name: "train", Image: "image"})
	if report.Supported {
		t.Fatalf("readiness-only provider accepted job: %#v", report)
	}
	if len(report.Reasons) == 0 || !strings.Contains(report.Reasons[0], "readiness-only") {
		t.Fatalf("reasons = %#v", report.Reasons)
	}
	result, err := provider.Submit(context.Background(), app.SubmitRequest{})
	if err == nil {
		t.Fatal("expected submit error")
	}
	if result.ExitCode == 0 || app.ProviderErrorKindOf(err) != app.ProviderErrorInvalidSpec {
		t.Fatalf("result=%#v err=%v", result, err)
	}
}

func TestEndpointOverride(t *testing.T) {
	def := Definitions()[0]
	t.Setenv(def.EndpointEnv, "https://example.invalid/")
	provider := New(def)
	if got := provider.endpoint(); got != "https://example.invalid/" {
		t.Fatalf("endpoint = %q", got)
	}
}

func TestMain(m *testing.M) {
	for _, def := range Definitions() {
		for _, req := range def.Requirements {
			for _, name := range req.Names {
				_ = os.Unsetenv(name)
			}
		}
		if def.EndpointEnv != "" {
			_ = os.Unsetenv(def.EndpointEnv)
		}
	}
	os.Exit(m.Run())
}
