package chinacloud

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/anthonylu23/switchboard-cli/internal/app"
)

const defaultAuthTimeout = 5 * time.Second

type EnvRequirement struct {
	Label string
	Names []string
}

type Definition struct {
	Name           string
	DisplayName    string
	Endpoint       string
	EndpointEnv    string
	AuthCommandEnv string
	Regions        []string
	URISchemes     []string
	Requirements   []EnvRequirement
	Documentation  string
}

type Provider struct {
	definition Definition
	client     *http.Client
}

type ConnectionOptions struct {
	RequireAuthenticated bool
}

type ConnectionReport struct {
	Mode            string
	Authenticated   bool
	Endpoint        string
	AuthCommandEnv  string
	BuiltInAuth     bool
	Documentation   string
	Warnings        []string
	CredentialNames []string
}

func New(definition Definition) *Provider {
	return &Provider{
		definition: definition,
		client:     &http.Client{Timeout: defaultAuthTimeout},
	}
}

func NewWithClient(definition Definition, client *http.Client) *Provider {
	if client == nil {
		client = &http.Client{Timeout: defaultAuthTimeout}
	}
	return &Provider{definition: definition, client: client}
}

func NewProviders() []app.ProviderAdapter {
	defs := Definitions()
	adapters := make([]app.ProviderAdapter, 0, len(defs))
	for _, def := range defs {
		adapters = append(adapters, New(def))
	}
	return adapters
}

func Definitions() []Definition {
	return []Definition{
		{
			Name:           "alibaba-cloud",
			DisplayName:    "Alibaba Cloud",
			Endpoint:       "https://ecs.cn-hangzhou.aliyuncs.com/",
			EndpointEnv:    "SWITCHBOARD_ALIBABA_CLOUD_ENDPOINT",
			AuthCommandEnv: "SWITCHBOARD_ALIBABA_CLOUD_AUTH_COMMAND",
			Regions:        []string{"cn-hangzhou", "cn-shanghai", "cn-beijing", "cn-shenzhen"},
			URISchemes:     []string{"oss"},
			Requirements: []EnvRequirement{
				{Label: "access key id", Names: []string{"ALIBABA_CLOUD_ACCESS_KEY_ID"}},
				{Label: "access key secret", Names: []string{"ALIBABA_CLOUD_ACCESS_KEY_SECRET"}},
			},
			Documentation: "https://www.alibabacloud.com/help/doc-detail/2392167.html",
		},
		{
			Name:           "huawei-cloud",
			DisplayName:    "Huawei Cloud",
			Endpoint:       "https://ecs.cn-north-4.myhuaweicloud.com/",
			EndpointEnv:    "SWITCHBOARD_HUAWEI_CLOUD_ENDPOINT",
			AuthCommandEnv: "SWITCHBOARD_HUAWEI_CLOUD_AUTH_COMMAND",
			Regions:        []string{"cn-north-4", "cn-east-3", "cn-south-1", "cn-southwest-2"},
			URISchemes:     []string{"obs"},
			Requirements: []EnvRequirement{
				{Label: "access key", Names: []string{"HUAWEICLOUD_SDK_AK", "HUAWEI_CLOUD_ACCESS_KEY_ID"}},
				{Label: "secret key", Names: []string{"HUAWEICLOUD_SDK_SK", "HUAWEI_CLOUD_SECRET_ACCESS_KEY"}},
			},
			Documentation: "https://support.huaweicloud.com/intl/en-us/devg-apisign/api-sign-provide01.html",
		},
		{
			Name:           "tencent-cloud",
			DisplayName:    "Tencent Cloud",
			Endpoint:       "https://cvm.tencentcloudapi.com/",
			EndpointEnv:    "SWITCHBOARD_TENCENT_CLOUD_ENDPOINT",
			AuthCommandEnv: "SWITCHBOARD_TENCENT_CLOUD_AUTH_COMMAND",
			Regions:        []string{"ap-beijing", "ap-shanghai", "ap-guangzhou", "ap-chengdu"},
			URISchemes:     []string{"cos"},
			Requirements: []EnvRequirement{
				{Label: "secret id", Names: []string{"TENCENTCLOUD_SECRET_ID"}},
				{Label: "secret key", Names: []string{"TENCENTCLOUD_SECRET_KEY"}},
			},
			Documentation: "https://cloud.tencent.com/document/sdk/Go",
		},
		{
			Name:           "tianyi-cloud",
			DisplayName:    "China Telecom Tianyi Cloud / eSurfing Cloud",
			Endpoint:       "https://ecx-global.ctapi.ctyun.cn/",
			EndpointEnv:    "SWITCHBOARD_TIANYI_CLOUD_ENDPOINT",
			AuthCommandEnv: "SWITCHBOARD_TIANYI_CLOUD_AUTH_COMMAND",
			Regions:        []string{"huadong1", "huabei2", "huanan1"},
			URISchemes:     []string{"ctyun", "oos"},
			Requirements: []EnvRequirement{
				{Label: "access key", Names: []string{"CTYUN_ACCESS_KEY", "TIANYI_CLOUD_ACCESS_KEY"}},
				{Label: "secret key", Names: []string{"CTYUN_SECRET_KEY", "TIANYI_CLOUD_SECRET_KEY"}},
			},
			Documentation: "https://www.ctyun.cn/document/10011497/10629581",
		},
		{
			Name:           "baidu-ai-cloud",
			DisplayName:    "Baidu AI Cloud",
			Endpoint:       "https://bj.bcebos.com/",
			EndpointEnv:    "SWITCHBOARD_BAIDU_AI_CLOUD_ENDPOINT",
			AuthCommandEnv: "SWITCHBOARD_BAIDU_AI_CLOUD_AUTH_COMMAND",
			Regions:        []string{"bj", "gz", "su", "hkg"},
			URISchemes:     []string{"bos", "bcebos"},
			Requirements: []EnvRequirement{
				{Label: "access key id", Names: []string{"BAIDU_CLOUD_ACCESS_KEY_ID", "BCE_ACCESS_KEY_ID"}},
				{Label: "secret access key", Names: []string{"BAIDU_CLOUD_SECRET_ACCESS_KEY", "BCE_SECRET_ACCESS_KEY"}},
			},
			Documentation: "https://intl.cloud.baidu.com/en/doc/BOS/s/Tjwvyrw7a-intl-en",
		},
	}
}

func Names() []string {
	defs := Definitions()
	names := make([]string, 0, len(defs))
	for _, def := range defs {
		names = append(names, def.Name)
	}
	sort.Strings(names)
	return names
}

func (p *Provider) Name() app.ProviderName {
	return app.ProviderName(p.definition.Name)
}

func (p *Provider) ValidateAuth(ctx context.Context) error {
	_, err := p.ValidateConnection(ctx, ConnectionOptions{})
	return err
}

func (p *Provider) ValidateConnection(ctx context.Context, opts ConnectionOptions) (ConnectionReport, error) {
	report := ConnectionReport{
		Endpoint:        p.endpoint(),
		AuthCommandEnv:  p.definition.AuthCommandEnv,
		BuiltInAuth:     p.hasBuiltInAuth(),
		Documentation:   p.definition.Documentation,
		CredentialNames: p.credentialNames(),
	}
	if command := strings.TrimSpace(os.Getenv(p.definition.AuthCommandEnv)); command != "" {
		report.Mode = "auth_command"
		report.Authenticated = true
		return report, p.runAuthCommand(ctx, command)
	}
	var missing []string
	for _, req := range p.definition.Requirements {
		if !anyEnvSet(req.Names) {
			missing = append(missing, fmt.Sprintf("%s (%s)", req.Label, strings.Join(req.Names, " or ")))
		}
	}
	if len(missing) > 0 {
		return report, &app.ProviderError{
			Kind:    app.ProviderErrorAuth,
			Message: fmt.Sprintf("%s credentials are not configured; missing %s", p.definition.DisplayName, strings.Join(missing, ", ")),
		}
	}
	if p.hasBuiltInAuth() {
		if err := p.runBuiltInAuth(ctx, report.Endpoint); err != nil {
			return report, err
		}
		report.Mode = "built_in_signed_request"
		report.Authenticated = true
		return report, nil
	}
	if opts.RequireAuthenticated {
		return report, &app.ProviderError{
			Kind:    app.ProviderErrorAuth,
			Message: fmt.Sprintf("%s authenticated validation requires %s to run an official CLI or SDK smoke command", p.definition.DisplayName, p.definition.AuthCommandEnv),
		}
	}
	endpoint := report.Endpoint
	if endpoint == "" {
		return report, &app.ProviderError{Kind: app.ProviderErrorInvalidSpec, Message: fmt.Sprintf("%s endpoint is empty", p.definition.DisplayName)}
	}
	ctx, cancel := context.WithTimeout(ctx, defaultAuthTimeout)
	defer cancel()
	if err := p.probeEndpoint(ctx, endpoint); err != nil {
		return report, &app.ProviderError{
			Kind:    app.ProviderErrorNetwork,
			Message: fmt.Sprintf("%s endpoint probe failed for %s: %v", p.definition.DisplayName, endpoint, err),
			Err:     err,
		}
	}
	report.Mode = "endpoint_probe"
	report.Authenticated = false
	report.Warnings = []string{
		"endpoint probe validates network reachability only; set the auth command env var or use providers check --strict-auth for authenticated validation",
	}
	return report, nil
}

func (p *Provider) runAuthCommand(ctx context.Context, command string) error {
	ctx, cancel := context.WithTimeout(ctx, defaultAuthTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return &app.ProviderError{
				Kind:    app.ProviderErrorNetwork,
				Message: fmt.Sprintf("%s auth command timed out", p.definition.DisplayName),
				Err:     ctx.Err(),
			}
		}
		return &app.ProviderError{
			Kind:    app.ProviderErrorAuth,
			Message: fmt.Sprintf("%s auth command failed: %v", p.definition.DisplayName, err),
			Err:     err,
		}
	}
	return nil
}

func (p *Provider) hasBuiltInAuth() bool {
	switch p.definition.Name {
	case "alibaba-cloud", "tencent-cloud", "baidu-ai-cloud":
		return true
	default:
		return false
	}
}

func (p *Provider) runBuiltInAuth(ctx context.Context, endpoint string) error {
	switch p.definition.Name {
	case "alibaba-cloud":
		return p.runAlibabaAuth(ctx, endpoint)
	case "tencent-cloud":
		return p.runTencentAuth(ctx, endpoint)
	case "baidu-ai-cloud":
		return p.runBaiduAuth(ctx, endpoint)
	default:
		return &app.ProviderError{
			Kind:    app.ProviderErrorInvalidSpec,
			Message: fmt.Sprintf("%s has no built-in authenticated validation", p.definition.DisplayName),
		}
	}
}

func (p *Provider) runAlibabaAuth(ctx context.Context, endpoint string) error {
	accessKeyID := envValue("ALIBABA_CLOUD_ACCESS_KEY_ID")
	accessKeySecret := envValue("ALIBABA_CLOUD_ACCESS_KEY_SECRET")
	values := map[string]string{
		"AccessKeyId":      accessKeyID,
		"Action":           "DescribeRegions",
		"Format":           "JSON",
		"SignatureMethod":  "HMAC-SHA1",
		"SignatureNonce":   strconv.FormatInt(time.Now().UnixNano(), 10),
		"SignatureVersion": "1.0",
		"Timestamp":        time.Now().UTC().Format("2006-01-02T15:04:05Z"),
		"Version":          "2014-05-26",
	}
	canonicalQuery := alibabaCanonicalQuery(values)
	stringToSign := "GET&%2F&" + alibabaPercentEncode(canonicalQuery)
	signature := base64.StdEncoding.EncodeToString(hmacDigest(sha1.New, []byte(accessKeySecret+"&"), []byte(stringToSign)))

	requestURL, err := url.Parse(endpoint)
	if err != nil {
		return &app.ProviderError{Kind: app.ProviderErrorInvalidSpec, Message: fmt.Sprintf("Alibaba Cloud endpoint is invalid: %v", err), Err: err}
	}
	query := requestURL.Query()
	for key, value := range values {
		query.Set(key, value)
	}
	query.Set("Signature", signature)
	requestURL.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return &app.ProviderError{Kind: app.ProviderErrorInvalidSpec, Message: fmt.Sprintf("create Alibaba Cloud request: %v", err), Err: err}
	}
	return p.doAuthRequest(req)
}

func (p *Provider) runTencentAuth(ctx context.Context, endpoint string) error {
	secretID := envValue("TENCENTCLOUD_SECRET_ID")
	secretKey := envValue("TENCENTCLOUD_SECRET_KEY")
	service := "cvm"
	action := "DescribeRegions"
	version := "2017-03-12"
	region := "ap-guangzhou"
	body := []byte("{}")
	requestURL, err := url.Parse(endpoint)
	if err != nil {
		return &app.ProviderError{Kind: app.ProviderErrorInvalidSpec, Message: fmt.Sprintf("Tencent Cloud endpoint is invalid: %v", err), Err: err}
	}
	if requestURL.Path == "" {
		requestURL.Path = "/"
	}

	now := time.Now().UTC()
	timestamp := strconv.FormatInt(now.Unix(), 10)
	date := now.Format("2006-01-02")
	contentType := "application/json; charset=utf-8"
	hashedPayload := sha256Hex(body)
	canonicalHeaders := fmt.Sprintf("content-type:%s\nhost:%s\nx-tc-action:%s\n", contentType, requestURL.Host, strings.ToLower(action))
	signedHeaders := "content-type;host;x-tc-action"
	canonicalRequest := strings.Join([]string{
		http.MethodPost,
		"/",
		"",
		canonicalHeaders,
		signedHeaders,
		hashedPayload,
	}, "\n")
	credentialScope := fmt.Sprintf("%s/%s/tc3_request", date, service)
	stringToSign := strings.Join([]string{
		"TC3-HMAC-SHA256",
		timestamp,
		credentialScope,
		sha256Hex([]byte(canonicalRequest)),
	}, "\n")
	secretDate := hmacDigest(sha256.New, []byte("TC3"+secretKey), []byte(date))
	secretService := hmacDigest(sha256.New, secretDate, []byte(service))
	secretSigning := hmacDigest(sha256.New, secretService, []byte("tc3_request"))
	signature := hex.EncodeToString(hmacDigest(sha256.New, secretSigning, []byte(stringToSign)))
	authorization := fmt.Sprintf(
		"TC3-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		secretID,
		credentialScope,
		signedHeaders,
		signature,
	)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL.String(), bytes.NewReader(body))
	if err != nil {
		return &app.ProviderError{Kind: app.ProviderErrorInvalidSpec, Message: fmt.Sprintf("create Tencent Cloud request: %v", err), Err: err}
	}
	req.Header.Set("Authorization", authorization)
	req.Header.Set("Content-Type", contentType)
	req.Host = requestURL.Host
	req.Header.Set("X-TC-Action", action)
	req.Header.Set("X-TC-Timestamp", timestamp)
	req.Header.Set("X-TC-Version", version)
	req.Header.Set("X-TC-Region", region)
	return p.doAuthRequest(req)
}

func (p *Provider) runBaiduAuth(ctx context.Context, endpoint string) error {
	accessKeyID := firstEnvValue("BAIDU_CLOUD_ACCESS_KEY_ID", "BCE_ACCESS_KEY_ID")
	secretAccessKey := firstEnvValue("BAIDU_CLOUD_SECRET_ACCESS_KEY", "BCE_SECRET_ACCESS_KEY")
	requestURL, err := url.Parse(endpoint)
	if err != nil {
		return &app.ProviderError{Kind: app.ProviderErrorInvalidSpec, Message: fmt.Sprintf("Baidu AI Cloud endpoint is invalid: %v", err), Err: err}
	}
	if requestURL.Path == "" {
		requestURL.Path = "/"
	}
	now := time.Now().UTC().Format("2006-01-02T15:04:05Z")
	expirationSeconds := "1800"
	signedHeaders := "host;x-bce-date"
	authPrefix := fmt.Sprintf("bce-auth-v1/%s/%s/%s", accessKeyID, now, expirationSeconds)
	signingKey := hex.EncodeToString(hmacDigest(sha256.New, []byte(secretAccessKey), []byte(authPrefix)))
	canonicalHeaders := fmt.Sprintf("host:%s\nx-bce-date:%s", requestURL.Host, now)
	canonicalRequest := strings.Join([]string{
		http.MethodGet,
		requestURL.EscapedPath(),
		requestURL.RawQuery,
		canonicalHeaders,
	}, "\n")
	signature := hex.EncodeToString(hmacDigest(sha256.New, []byte(signingKey), []byte(canonicalRequest)))
	authorization := fmt.Sprintf("%s/%s/%s", authPrefix, signedHeaders, signature)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return &app.ProviderError{Kind: app.ProviderErrorInvalidSpec, Message: fmt.Sprintf("create Baidu AI Cloud request: %v", err), Err: err}
	}
	req.Header.Set("Authorization", authorization)
	req.Host = requestURL.Host
	req.Header.Set("x-bce-date", now)
	return p.doAuthRequest(req)
}

func (p *Provider) doAuthRequest(req *http.Request) error {
	ctx, cancel := context.WithTimeout(req.Context(), defaultAuthTimeout)
	defer cancel()
	req = req.WithContext(ctx)
	resp, err := p.client.Do(req)
	if err != nil {
		return &app.ProviderError{Kind: app.ProviderErrorNetwork, Message: fmt.Sprintf("%s signed auth request failed: %v", p.definition.DisplayName, err), Err: err}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return nil
	}
	if resp.StatusCode >= 500 {
		return &app.ProviderError{Kind: app.ProviderErrorInternal, Message: fmt.Sprintf("%s signed auth request returned HTTP %d", p.definition.DisplayName, resp.StatusCode)}
	}
	if isAuthFailureBody(body) || resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return &app.ProviderError{Kind: app.ProviderErrorAuth, Message: fmt.Sprintf("%s signed auth request was rejected with HTTP %d", p.definition.DisplayName, resp.StatusCode)}
	}
	return &app.ProviderError{Kind: app.ProviderErrorUnknown, Message: fmt.Sprintf("%s signed auth request returned HTTP %d", p.definition.DisplayName, resp.StatusCode)}
}

func isAuthFailureBody(body []byte) bool {
	lower := strings.ToLower(string(body))
	for _, marker := range []string{
		"authfailure",
		"signature",
		"invalidaccesskey",
		"invalid_access_key",
		"invalidcredential",
		"invalid credential",
		"secretid",
		"accesskeyidnotfound",
		"invalidaccesskeyid",
		"signaturedoesnotmatch",
	} {
		if strings.Contains(lower, strings.ToLower(marker)) {
			return true
		}
	}
	return false
}

func (p *Provider) Capabilities(ctx context.Context) (app.ProviderCapabilities, error) {
	return app.ProviderCapabilities{
		GPUFamilies:                nil,
		Regions:                    append([]string(nil), p.definition.Regions...),
		HardwareShapes:             nil,
		SupportsSpot:               false,
		SupportsOnDemand:           false,
		SupportsDockerImage:        false,
		SupportsLocalScript:        false,
		SupportsDataBundle:         false,
		SupportedURISchemes:        append([]string(nil), p.definition.URISchemes...),
		SupportedCheckpointSchemes: append([]string(nil), p.definition.URISchemes...),
		SupportsObjectStorePull:    false,
	}, nil
}

func (p *Provider) ValidateJob(ctx context.Context, spec app.JobSpec) app.SupportReport {
	return app.SupportReport{
		Supported: false,
		Reasons: []string{
			fmt.Sprintf("%s is a readiness-only provider; job submission is not implemented yet", p.definition.Name),
		},
	}
}

func (p *Provider) Estimate(ctx context.Context, spec app.JobSpec) (app.CostEstimate, error) {
	return app.CostEstimate{HourlyUSD: 0, Currency: "USD"}, nil
}

func (p *Provider) Submit(ctx context.Context, req app.SubmitRequest) (app.SubmitResult, error) {
	err := &app.ProviderError{
		Kind:    app.ProviderErrorInvalidSpec,
		Message: fmt.Sprintf("%s does not implement job submission yet", p.definition.Name),
	}
	return app.SubmitResult{ExitCode: 10, ExitReason: err.Error()}, err
}

func (p *Provider) GetStatus(ctx context.Context, ref app.ProviderJobRef) (app.ProviderJobStatus, error) {
	return app.ProviderJobStatus{}, &app.ProviderError{
		Kind:    app.ProviderErrorInvalidSpec,
		Message: fmt.Sprintf("%s does not create provider job refs yet", p.definition.Name),
	}
}

func (p *Provider) StreamLogs(ctx context.Context, req app.LogStreamRequest) (app.LogStream, error) {
	return nil, fmt.Errorf("%s logs are unavailable because job submission is not implemented", p.definition.Name)
}

func (p *Provider) Cancel(ctx context.Context, ref app.ProviderJobRef) error {
	return &app.ProviderError{
		Kind:    app.ProviderErrorInvalidSpec,
		Message: fmt.Sprintf("%s cancel is unavailable because job submission is not implemented", p.definition.Name),
	}
}

func (p *Provider) endpoint() string {
	if p.definition.EndpointEnv != "" {
		if value := strings.TrimSpace(os.Getenv(p.definition.EndpointEnv)); value != "" {
			return value
		}
	}
	return p.definition.Endpoint
}

func (p *Provider) probeEndpoint(ctx context.Context, endpoint string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, endpoint, nil)
	if err != nil {
		return err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode == http.StatusMethodNotAllowed {
		return p.probeEndpointWithGet(ctx, endpoint)
	}
	if resp.StatusCode >= 500 {
		return fmt.Errorf("unexpected HTTP %d", resp.StatusCode)
	}
	return nil
}

func (p *Provider) probeEndpointWithGet(ctx context.Context, endpoint string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 500 {
		return fmt.Errorf("unexpected HTTP %d", resp.StatusCode)
	}
	return nil
}

func anyEnvSet(names []string) bool {
	for _, name := range names {
		if strings.TrimSpace(os.Getenv(name)) != "" {
			return true
		}
	}
	return false
}

func envValue(name string) string {
	return strings.TrimSpace(os.Getenv(name))
}

func firstEnvValue(names ...string) string {
	for _, name := range names {
		if value := envValue(name); value != "" {
			return value
		}
	}
	return ""
}

func (p *Provider) credentialNames() []string {
	var names []string
	for _, req := range p.definition.Requirements {
		names = append(names, req.Names...)
	}
	sort.Strings(names)
	return names
}

func IsReadinessOnly(adapter app.ProviderAdapter) bool {
	provider, ok := adapter.(*Provider)
	return ok && provider != nil
}

func DefinitionFor(name string) (Definition, error) {
	for _, def := range Definitions() {
		if def.Name == name {
			return def, nil
		}
	}
	return Definition{}, errors.New("unknown China cloud provider")
}

func alibabaCanonicalQuery(values map[string]string) string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, alibabaPercentEncode(key)+"="+alibabaPercentEncode(values[key]))
	}
	return strings.Join(parts, "&")
}

func alibabaPercentEncode(value string) string {
	escaped := url.QueryEscape(value)
	escaped = strings.ReplaceAll(escaped, "+", "%20")
	escaped = strings.ReplaceAll(escaped, "*", "%2A")
	escaped = strings.ReplaceAll(escaped, "%7E", "~")
	return escaped
}

func hmacDigest(hash func() hash.Hash, key []byte, data []byte) []byte {
	mac := hmac.New(hash, key)
	_, _ = mac.Write(data)
	return mac.Sum(nil)
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
