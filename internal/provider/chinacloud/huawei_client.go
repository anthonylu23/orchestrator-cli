package chinacloud

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/anthonylu23/switchboard-cli/internal/app"
	"github.com/anthonylu23/switchboard-cli/internal/credentials"
)

type HuaweiConfig struct {
	Region                   string
	Zone                     string
	ProjectID                string
	FlavorRef                string
	ImageRef                 string
	VPCID                    string
	SubnetID                 string
	SecurityGroupID          string
	KeyName                  string
	SSHUser                  string
	SSHPrivateKey            string
	RootVolumeType           string
	RootVolumeSizeGB         int
	PollIntervalSeconds      int
	SSHConnectTimeoutSecs    int
	SSHReadyTimeoutSeconds   int
	TerminateOnCompletion    bool
	TerminateOnCompletionSet bool
	KeepInstanceOnFailure    bool
	EstimateHourlyUSD        float64
	Endpoint                 string
	APITimeoutSeconds        int
	AccessKey                string
	SecretKey                string
	Credentials              credentials.Resolver
	HardwareShapes           []app.HardwareShape
}

type huaweiClient struct {
	config     HuaweiConfig
	httpClient *http.Client
}

func NewHuawei(config HuaweiConfig, stdout io.Writer, stderr io.Writer) *Provider {
	return NewHuaweiWithClient(config, newHuaweiClient(config), nil, stdout, stderr)
}

func NewHuaweiWithClient(config HuaweiConfig, client VMClient, remote VMRemoteRunner, stdout io.Writer, stderr io.Writer) *Provider {
	def, _ := DefinitionFor("huawei-cloud")
	provider := New(def)
	provider.runtime = newVMRuntime(huaweiRuntimeConfig(config), client, remote)
	provider.Stdout = stdout
	provider.Stderr = stderr
	return provider
}

func newHuaweiClient(config HuaweiConfig) VMClient {
	config = huaweiConfigDefaults(config)
	return &huaweiClient{config: config, httpClient: requestHTTPClient(config.APITimeoutSeconds)}
}

func huaweiConfigDefaults(config HuaweiConfig) HuaweiConfig {
	if config.Region == "" {
		config.Region = "cn-north-4"
	}
	if config.Endpoint == "" {
		config.Endpoint = fmt.Sprintf("https://ecs.%s.myhuaweicloud.com", config.Region)
	}
	if config.RootVolumeType == "" {
		config.RootVolumeType = "SSD"
	}
	if config.RootVolumeSizeGB == 0 {
		config.RootVolumeSizeGB = 80
	}
	return config
}

func huaweiRuntimeConfig(config HuaweiConfig) VMRuntimeConfig {
	config = huaweiConfigDefaults(config)
	return VMRuntimeConfig{
		Region:                   config.Region,
		Zone:                     config.Zone,
		InstanceType:             config.FlavorRef,
		ImageID:                  config.ImageRef,
		SSHUser:                  config.SSHUser,
		SSHPrivateKey:            config.SSHPrivateKey,
		SSHKeyName:               config.KeyName,
		NetworkID:                config.VPCID,
		SubnetID:                 config.SubnetID,
		SecurityGroupID:          config.SecurityGroupID,
		SystemDiskType:           config.RootVolumeType,
		SystemDiskSizeGB:         config.RootVolumeSizeGB,
		PollIntervalSeconds:      config.PollIntervalSeconds,
		SSHConnectTimeoutSecs:    config.SSHConnectTimeoutSecs,
		SSHReadyTimeoutSeconds:   config.SSHReadyTimeoutSeconds,
		TerminateOnCompletion:    config.TerminateOnCompletion,
		TerminateOnCompletionSet: config.TerminateOnCompletionSet,
		KeepInstanceOnFailure:    config.KeepInstanceOnFailure,
		EstimateHourlyUSD:        config.EstimateHourlyUSD,
		ProjectOrAccount:         config.ProjectID,
		RequireProjectOrAccount:  true,
		HardwareShapes:           config.HardwareShapes,
	}
}

func (c *huaweiClient) ValidateAuth(ctx context.Context) error {
	accessKey, secretKey, err := c.credentials()
	if err != nil {
		return err
	}
	if err := c.requireProjectID(); err != nil {
		return err
	}
	request, err := newHuaweiSignedRequest(ctx, c.config.Endpoint, http.MethodGet, c.cloudserversPath()+"/detail", url.Values{"limit": []string{"1"}}, nil, accessKey, secretKey)
	if err != nil {
		return err
	}
	return c.do(request, nil)
}

func (c *huaweiClient) CreateVM(ctx context.Context, req VMCreateRequest) (VMInstance, error) {
	accessKey, secretKey, err := c.credentials()
	if err != nil {
		return VMInstance{}, err
	}
	if err := c.requireProjectID(); err != nil {
		return VMInstance{}, err
	}
	body, err := json.Marshal(huaweiCreatePayload(req))
	if err != nil {
		return VMInstance{}, err
	}
	request, err := newHuaweiSignedRequest(ctx, c.config.Endpoint, http.MethodPost, c.cloudserversPath(), nil, body, accessKey, secretKey)
	if err != nil {
		return VMInstance{}, err
	}
	var out struct {
		Server struct {
			ID string `json:"id"`
		} `json:"server"`
		JobID string `json:"job_id"`
	}
	if err := c.do(request, &out); err != nil {
		return VMInstance{}, err
	}
	if out.Server.ID == "" {
		return VMInstance{}, &app.ProviderError{Kind: app.ProviderErrorInternal, Message: "Huawei Cloud ECS create returned no server ID"}
	}
	return VMInstance{ID: out.Server.ID, State: VMStatePending, NativeState: "BUILD", Region: req.Region, Zone: req.Zone}, nil
}

func (c *huaweiClient) GetVM(ctx context.Context, id string) (VMInstance, error) {
	accessKey, secretKey, err := c.credentials()
	if err != nil {
		return VMInstance{}, err
	}
	if err := c.requireProjectID(); err != nil {
		return VMInstance{}, err
	}
	request, err := newHuaweiSignedRequest(ctx, c.config.Endpoint, http.MethodGet, c.cloudserversPath()+"/"+url.PathEscape(id), nil, nil, accessKey, secretKey)
	if err != nil {
		return VMInstance{}, err
	}
	var out struct {
		Server struct {
			ID        string `json:"id"`
			Status    string `json:"status"`
			TenantID  string `json:"tenant_id"`
			Addresses map[string][]struct {
				Addr    string `json:"addr"`
				Version int    `json:"version"`
				Type    string `json:"OS-EXT-IPS:type"`
			} `json:"addresses"`
		} `json:"server"`
	}
	if err := c.do(request, &out); err != nil {
		return VMInstance{}, err
	}
	if out.Server.ID == "" {
		return VMInstance{}, &app.ProviderError{Kind: app.ProviderErrorInvalidSpec, Message: fmt.Sprintf("Huawei Cloud ECS server %q was not found", id)}
	}
	publicIP, privateIP := huaweiIPs(out.Server.Addresses)
	return VMInstance{
		ID:          out.Server.ID,
		State:       huaweiClientVMState(out.Server.Status),
		NativeState: out.Server.Status,
		PublicIP:    publicIP,
		PrivateIP:   privateIP,
		Region:      c.config.Region,
	}, nil
}

func (c *huaweiClient) TerminateVM(ctx context.Context, id string) error {
	accessKey, secretKey, err := c.credentials()
	if err != nil {
		return err
	}
	if err := c.requireProjectID(); err != nil {
		return err
	}
	request, err := newHuaweiSignedRequest(ctx, c.config.Endpoint, http.MethodDelete, c.cloudserversPath()+"/"+url.PathEscape(id), nil, nil, accessKey, secretKey)
	if err != nil {
		return err
	}
	return c.do(request, nil)
}

func (c *huaweiClient) credentials() (string, string, error) {
	accessKey := strings.TrimSpace(c.config.AccessKey)
	secretKey := strings.TrimSpace(c.config.SecretKey)
	if accessKey == "" {
		if secret, err := c.config.Credentials.Resolve(credentials.Query{Provider: "huawei-cloud", Name: "access_key_id", Env: []string{"HUAWEICLOUD_SDK_AK", "HUAWEI_CLOUD_ACCESS_KEY_ID"}}); err == nil {
			accessKey = secret.Value
		}
	}
	if secretKey == "" {
		if secret, err := c.config.Credentials.Resolve(credentials.Query{Provider: "huawei-cloud", Name: "secret_access_key", Env: []string{"HUAWEICLOUD_SDK_SK", "HUAWEI_CLOUD_SECRET_ACCESS_KEY"}}); err == nil {
			secretKey = secret.Value
		}
	}
	if accessKey == "" || secretKey == "" {
		return "", "", &app.ProviderError{Kind: app.ProviderErrorAuth, Message: "Huawei Cloud access_key_id and secret_access_key credentials are required"}
	}
	return accessKey, secretKey, nil
}

func (c *huaweiClient) requireProjectID() error {
	if strings.TrimSpace(c.config.ProjectID) == "" {
		return &app.ProviderError{Kind: app.ProviderErrorInvalidSpec, Message: "Huawei Cloud project_id is required"}
	}
	return nil
}

func (c *huaweiClient) cloudserversPath() string {
	return "/v1/" + url.PathEscape(c.config.ProjectID) + "/cloudservers"
}

func newHuaweiSignedRequest(ctx context.Context, endpoint string, method string, path string, query url.Values, body []byte, accessKey string, secretKey string) (*http.Request, error) {
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	requestURL, err := url.Parse(strings.TrimRight(endpoint, "/") + path)
	if err != nil {
		return nil, &app.ProviderError{Kind: app.ProviderErrorInvalidSpec, Message: fmt.Sprintf("Huawei Cloud endpoint is invalid: %v", err), Err: err}
	}
	if query != nil {
		requestURL.RawQuery = query.Encode()
	}
	timestamp := time.Now().UTC().Format("20060102T150405Z")
	contentType := "application/json"
	canonicalHeaders := fmt.Sprintf("content-type:%s\nhost:%s\nx-sdk-date:%s\n", contentType, requestURL.Host, timestamp)
	signedHeaders := "content-type;host;x-sdk-date"
	canonicalRequest := strings.Join([]string{
		method,
		canonicalPath(requestURL.EscapedPath()),
		canonicalQueryString(requestURL.Query()),
		canonicalHeaders,
		signedHeaders,
		sha256Hex(body),
	}, "\n")
	stringToSign := strings.Join([]string{
		"SDK-HMAC-SHA256",
		timestamp,
		sha256Hex([]byte(canonicalRequest)),
	}, "\n")
	signature := hex.EncodeToString(hmacDigest(sha256.New, []byte(secretKey), []byte(stringToSign)))
	authorization := fmt.Sprintf("SDK-HMAC-SHA256 Access=%s, SignedHeaders=%s, Signature=%s", accessKey, signedHeaders, signature)

	httpReq, err := http.NewRequestWithContext(ctx, method, requestURL.String(), bytes.NewReader(body))
	if err != nil {
		return nil, &app.ProviderError{Kind: app.ProviderErrorInvalidSpec, Message: fmt.Sprintf("create Huawei Cloud request: %v", err), Err: err}
	}
	httpReq.Host = requestURL.Host
	httpReq.Header.Set("Authorization", authorization)
	httpReq.Header.Set("Content-Type", contentType)
	httpReq.Header.Set("X-Sdk-Date", timestamp)
	return httpReq, nil
}

func (c *huaweiClient) do(req *http.Request, out interface{}) error {
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return &app.ProviderError{Kind: app.ProviderErrorNetwork, Message: fmt.Sprintf("Huawei Cloud ECS request failed: %v", err), Err: err}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	if err := huaweiResponseError(resp.StatusCode, body); err != nil {
		return err
	}
	if out == nil || len(body) == 0 {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return &app.ProviderError{Kind: app.ProviderErrorInternal, Message: fmt.Sprintf("decode Huawei Cloud ECS response: %v", err), Err: err}
	}
	return nil
}

func huaweiCreatePayload(req VMCreateRequest) map[string]interface{} {
	server := map[string]interface{}{
		"name":      req.Name,
		"imageRef":  req.ImageID,
		"flavorRef": req.InstanceType,
		"key_name":  req.SSHKeyName,
		"user_data": base64.StdEncoding.EncodeToString([]byte(req.UserData)),
	}
	if req.NetworkID != "" {
		server["vpcid"] = req.NetworkID
	}
	if req.SubnetID != "" {
		server["nics"] = []map[string]string{{"subnet_id": req.SubnetID}}
	}
	if req.SecurityGroupID != "" {
		server["security_groups"] = []map[string]string{{"id": req.SecurityGroupID}}
	}
	if req.SystemDiskType != "" || req.SystemDiskSizeGB > 0 {
		root := map[string]interface{}{}
		if req.SystemDiskType != "" {
			root["volumetype"] = req.SystemDiskType
		}
		if req.SystemDiskSizeGB > 0 {
			root["size"] = req.SystemDiskSizeGB
		}
		server["root_volume"] = root
	}
	return map[string]interface{}{"server": server}
}

func huaweiResponseError(statusCode int, body []byte) error {
	var parsed struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
		ErrorCode string `json:"error_code"`
		ErrorMsg  string `json:"error_msg"`
	}
	_ = json.Unmarshal(body, &parsed)
	code := parsed.Error.Code
	message := parsed.Error.Message
	if code == "" {
		code = parsed.ErrorCode
	}
	if message == "" {
		message = parsed.ErrorMsg
	}
	if statusCode >= 200 && statusCode < 300 && code == "" {
		return nil
	}
	if message == "" {
		message = strings.TrimSpace(string(body))
	}
	if message == "" {
		message = fmt.Sprintf("HTTP %d", statusCode)
	}
	return &app.ProviderError{Kind: huaweiErrorKind(statusCode, code, message), Message: "Huawei Cloud ECS API: " + message}
}

func huaweiErrorKind(statusCode int, code string, message string) app.ProviderErrorKind {
	lower := strings.ToLower(code + " " + message)
	switch {
	case statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden || strings.Contains(lower, "auth") || strings.Contains(lower, "signature") || strings.Contains(lower, "aksk"):
		return app.ProviderErrorAuth
	case statusCode == http.StatusTooManyRequests || strings.Contains(lower, "quota"):
		return app.ProviderErrorQuota
	case strings.Contains(lower, "insufficient") || strings.Contains(lower, "no valid host") || strings.Contains(lower, "capacity"):
		return app.ProviderErrorCapacity
	case statusCode == http.StatusBadRequest || strings.Contains(lower, "invalid"):
		return app.ProviderErrorInvalidSpec
	case statusCode >= 500:
		return app.ProviderErrorInternal
	default:
		return app.ProviderErrorUnknown
	}
}

func huaweiClientVMState(status string) VMState {
	switch strings.ToLower(status) {
	case "build", "building", "reboot", "hard_reboot", "resize", "verify_resize", "migrating":
		return VMStatePending
	case "active":
		return VMStateRunning
	case "deleting":
		return VMStateTerminating
	case "deleted", "shutoff", "shelved_offloaded":
		return VMStateTerminated
	case "error":
		return VMStateFailed
	default:
		return VMStateUnknown
	}
}

func huaweiIPs(addresses map[string][]struct {
	Addr    string `json:"addr"`
	Version int    `json:"version"`
	Type    string `json:"OS-EXT-IPS:type"`
}) (string, string) {
	var publicIP string
	var privateIP string
	for _, values := range addresses {
		for _, value := range values {
			if value.Addr == "" {
				continue
			}
			switch strings.ToLower(value.Type) {
			case "floating":
				if publicIP == "" {
					publicIP = value.Addr
				}
			case "fixed":
				if privateIP == "" {
					privateIP = value.Addr
				}
			default:
				if publicIP == "" {
					publicIP = value.Addr
				}
			}
		}
	}
	return publicIP, privateIP
}
