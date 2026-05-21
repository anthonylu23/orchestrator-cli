package chinacloud

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"sort"
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
