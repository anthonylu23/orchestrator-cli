package chinacloud

import (
	"context"
	"io"
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

func TestValidateConnectionUsesAlibabaBuiltInSignedAuth(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("method = %s, want GET", r.Method)
		}
		query := r.URL.Query()
		if query.Get("Action") != "DescribeRegions" || query.Get("AccessKeyId") != "ak-value" || query.Get("Signature") == "" {
			t.Fatalf("query = %s", r.URL.RawQuery)
		}
		if strings.Contains(r.URL.RawQuery, "secret-value") {
			t.Fatalf("request leaked secret: %s", r.URL.RawQuery)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"Regions":{"Region":[]}}`))
	}))
	defer server.Close()

	def := mustDefinition(t, "alibaba-cloud")
	def.Endpoint = server.URL + "/"
	t.Setenv("ALIBABA_CLOUD_ACCESS_KEY_ID", "ak-value")
	t.Setenv("ALIBABA_CLOUD_ACCESS_KEY_SECRET", "secret-value")
	report, err := New(def).ValidateConnection(context.Background(), ConnectionOptions{RequireAuthenticated: true})
	if err != nil {
		t.Fatalf("ValidateConnection returned error: %v", err)
	}
	if report.Mode != "built_in_signed_request" || !report.Authenticated || !report.BuiltInAuth {
		t.Fatalf("report = %#v", report)
	}
}

func TestValidateConnectionUsesHuaweiBuiltInSignedAuth(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || !strings.HasSuffix(r.URL.Path, "/v3/regions/cn-north-4") {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if auth := r.Header.Get("Authorization"); !strings.HasPrefix(auth, "SDK-HMAC-SHA256 Access=ak-value, SignedHeaders=content-type;host;x-sdk-date, Signature=") || strings.Contains(auth, "secret-value") {
			t.Fatalf("Authorization = %q", auth)
		}
		if r.Header.Get("X-Sdk-Date") == "" {
			t.Fatalf("missing X-Sdk-Date")
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"region":{"id":"cn-north-4"}}`))
	}))
	defer server.Close()

	def := mustDefinition(t, "huawei-cloud")
	def.BuiltInAuthEndpoint = server.URL + "/v3/regions/cn-north-4"
	t.Setenv("HUAWEICLOUD_SDK_AK", "ak-value")
	t.Setenv("HUAWEICLOUD_SDK_SK", "secret-value")
	report, err := New(def).ValidateConnection(context.Background(), ConnectionOptions{RequireAuthenticated: true})
	if err != nil {
		t.Fatalf("ValidateConnection returned error: %v", err)
	}
	if report.Mode != "built_in_signed_request" || !report.Authenticated || !report.BuiltInAuth || report.BuiltInEndpoint == "" {
		t.Fatalf("report = %#v", report)
	}
}

func TestValidateConnectionUsesTencentBuiltInSignedAuth(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodPost || string(body) != "{}" {
			t.Fatalf("method/body = %s/%q", r.Method, string(body))
		}
		if got := r.Header.Get("X-TC-Action"); got != "DescribeRegions" {
			t.Fatalf("X-TC-Action = %q", got)
		}
		if auth := r.Header.Get("Authorization"); !strings.HasPrefix(auth, "TC3-HMAC-SHA256 Credential=secret-id/") || strings.Contains(auth, "secret-key") {
			t.Fatalf("Authorization = %q", auth)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"Response":{"RegionSet":[],"RequestId":"req"}}`))
	}))
	defer server.Close()

	def := mustDefinition(t, "tencent-cloud")
	def.Endpoint = server.URL + "/"
	t.Setenv("TENCENTCLOUD_SECRET_ID", "secret-id")
	t.Setenv("TENCENTCLOUD_SECRET_KEY", "secret-key")
	report, err := New(def).ValidateConnection(context.Background(), ConnectionOptions{RequireAuthenticated: true})
	if err != nil {
		t.Fatalf("ValidateConnection returned error: %v", err)
	}
	if report.Mode != "built_in_signed_request" || !report.Authenticated || !report.BuiltInAuth {
		t.Fatalf("report = %#v", report)
	}
}

func TestValidateConnectionUsesTianyiBuiltInSignedAuth(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v3/cluster/describeRegionClusters" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if auth := r.Header.Get("Eop-Authorization"); !strings.HasPrefix(auth, "ak-value Headers=ctyun-eop-request-id;eop-date Signature=") || strings.Contains(auth, "secret-value") {
			t.Fatalf("Eop-Authorization = %q", auth)
		}
		if r.Header.Get("ctyun-eop-request-id") == "" || r.Header.Get("Eop-date") == "" {
			t.Fatalf("missing Tianyi auth headers")
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"code":0,"data":[]}`))
	}))
	defer server.Close()

	def := mustDefinition(t, "tianyi-cloud")
	def.BuiltInAuthEndpoint = server.URL + "/v3/cluster/describeRegionClusters"
	t.Setenv("CTYUN_ACCESS_KEY", "ak-value")
	t.Setenv("CTYUN_SECRET_KEY", "secret-value")
	report, err := New(def).ValidateConnection(context.Background(), ConnectionOptions{RequireAuthenticated: true})
	if err != nil {
		t.Fatalf("ValidateConnection returned error: %v", err)
	}
	if report.Mode != "built_in_signed_request" || !report.Authenticated || !report.BuiltInAuth || report.BuiltInEndpoint == "" {
		t.Fatalf("report = %#v", report)
	}
}

func TestValidateConnectionUsesBaiduBuiltInSignedAuth(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("method = %s, want GET", r.Method)
		}
		if auth := r.Header.Get("Authorization"); !strings.HasPrefix(auth, "bce-auth-v1/ak-value/") || strings.Contains(auth, "secret-value") {
			t.Fatalf("Authorization = %q", auth)
		}
		if r.Header.Get("x-bce-date") == "" {
			t.Fatalf("missing x-bce-date")
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`<ListAllMyBucketsResult/>`))
	}))
	defer server.Close()

	def := mustDefinition(t, "baidu-ai-cloud")
	def.Endpoint = server.URL + "/"
	t.Setenv("BAIDU_CLOUD_ACCESS_KEY_ID", "ak-value")
	t.Setenv("BAIDU_CLOUD_SECRET_ACCESS_KEY", "secret-value")
	report, err := New(def).ValidateConnection(context.Background(), ConnectionOptions{RequireAuthenticated: true})
	if err != nil {
		t.Fatalf("ValidateConnection returned error: %v", err)
	}
	if report.Mode != "built_in_signed_request" || !report.Authenticated || !report.BuiltInAuth {
		t.Fatalf("report = %#v", report)
	}
}

func TestValidateConnectionBuiltInSignedAuthFailure(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusUnauthorized} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				_, _ = w.Write([]byte(`{"Response":{"Error":{"Code":"AuthFailure.SecretIdNotFound"}}}`))
			}))
			defer server.Close()

			def := mustDefinition(t, "tencent-cloud")
			def.Endpoint = server.URL + "/"
			t.Setenv("TENCENTCLOUD_SECRET_ID", "secret-id")
			t.Setenv("TENCENTCLOUD_SECRET_KEY", "secret-key")
			_, err := New(def).ValidateConnection(context.Background(), ConnectionOptions{RequireAuthenticated: true})
			if err == nil {
				t.Fatal("expected built-in auth failure")
			}
			if app.ProviderErrorKindOf(err) != app.ProviderErrorAuth {
				t.Fatalf("kind = %s, err = %v", app.ProviderErrorKindOf(err), err)
			}
		})
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

func mustDefinition(t *testing.T, name string) Definition {
	t.Helper()
	def, err := DefinitionFor(name)
	if err != nil {
		t.Fatal(err)
	}
	return def
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
