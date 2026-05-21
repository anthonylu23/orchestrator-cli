package chinacloud

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/anthonylu23/switchboard-cli/internal/app"
	"github.com/anthonylu23/switchboard-cli/internal/credentials"
)

type AlibabaConfig struct {
	RegionID                 string
	ZoneID                   string
	InstanceType             string
	ImageID                  string
	SecurityGroupID          string
	VSwitchID                string
	KeyPairName              string
	SSHUser                  string
	SSHPrivateKey            string
	SystemDiskCategory       string
	SystemDiskSizeGB         int
	InternetMaxBandwidthOut  int
	PollIntervalSeconds      int
	SSHConnectTimeoutSecs    int
	SSHReadyTimeoutSeconds   int
	TerminateOnCompletion    bool
	TerminateOnCompletionSet bool
	KeepInstanceOnFailure    bool
	EstimateHourlyUSD        float64
	Endpoint                 string
	APITimeoutSeconds        int
	AccessKeyID              string
	AccessKeySecret          string
	Credentials              credentials.Resolver
	HardwareShapes           []app.HardwareShape
}

type alibabaClient struct {
	config     AlibabaConfig
	httpClient *http.Client
}

func NewAlibaba(config AlibabaConfig, stdout io.Writer, stderr io.Writer) *Provider {
	return NewAlibabaWithClient(config, newAlibabaClient(config), nil, stdout, stderr)
}

func NewAlibabaWithClient(config AlibabaConfig, client VMClient, remote VMRemoteRunner, stdout io.Writer, stderr io.Writer) *Provider {
	def, _ := DefinitionFor("alibaba-cloud")
	provider := New(def)
	provider.runtime = newVMRuntime(alibabaRuntimeConfig(config), client, remote)
	provider.Stdout = stdout
	provider.Stderr = stderr
	return provider
}

func newAlibabaClient(config AlibabaConfig) VMClient {
	return &alibabaClient{config: alibabaConfigDefaults(config), httpClient: requestHTTPClient(config.APITimeoutSeconds)}
}

func alibabaConfigDefaults(config AlibabaConfig) AlibabaConfig {
	if config.RegionID == "" {
		config.RegionID = "cn-hangzhou"
	}
	if config.Endpoint == "" {
		config.Endpoint = fmt.Sprintf("https://ecs.%s.aliyuncs.com/", config.RegionID)
	}
	if config.SystemDiskCategory == "" {
		config.SystemDiskCategory = "cloud_essd"
	}
	if config.SystemDiskSizeGB == 0 {
		config.SystemDiskSizeGB = 80
	}
	return config
}

func alibabaRuntimeConfig(config AlibabaConfig) VMRuntimeConfig {
	config = alibabaConfigDefaults(config)
	return VMRuntimeConfig{
		Region:                   config.RegionID,
		Zone:                     config.ZoneID,
		InstanceType:             config.InstanceType,
		ImageID:                  config.ImageID,
		SSHUser:                  config.SSHUser,
		SSHPrivateKey:            config.SSHPrivateKey,
		SSHKeyName:               config.KeyPairName,
		VSwitchID:                config.VSwitchID,
		SecurityGroupID:          config.SecurityGroupID,
		SystemDiskType:           config.SystemDiskCategory,
		SystemDiskSizeGB:         config.SystemDiskSizeGB,
		InternetBandwidthMbps:    config.InternetMaxBandwidthOut,
		PollIntervalSeconds:      config.PollIntervalSeconds,
		SSHConnectTimeoutSecs:    config.SSHConnectTimeoutSecs,
		SSHReadyTimeoutSeconds:   config.SSHReadyTimeoutSeconds,
		TerminateOnCompletion:    config.TerminateOnCompletion,
		TerminateOnCompletionSet: config.TerminateOnCompletionSet,
		KeepInstanceOnFailure:    config.KeepInstanceOnFailure,
		EstimateHourlyUSD:        config.EstimateHourlyUSD,
		HardwareShapes:           config.HardwareShapes,
	}
}

func (c *alibabaClient) ValidateAuth(ctx context.Context) error {
	accessKeyID, accessKeySecret, err := c.credentials()
	if err != nil {
		return err
	}
	req, err := newAlibabaSignedRequest(ctx, c.config.Endpoint, accessKeyID, accessKeySecret, map[string]string{
		"Action":  "DescribeRegions",
		"Version": "2014-05-26",
	})
	if err != nil {
		return err
	}
	return c.do(req, nil)
}

func (c *alibabaClient) CreateVM(ctx context.Context, req VMCreateRequest) (VMInstance, error) {
	accessKeyID, accessKeySecret, err := c.credentials()
	if err != nil {
		return VMInstance{}, err
	}
	values := map[string]string{
		"Action":                  "RunInstances",
		"Version":                 "2014-05-26",
		"RegionId":                req.Region,
		"ImageId":                 req.ImageID,
		"InstanceType":            req.InstanceType,
		"SecurityGroupId":         req.SecurityGroupID,
		"InstanceName":            req.Name,
		"HostName":                req.Name,
		"Amount":                  "1",
		"InstanceChargeType":      "PostPaid",
		"UserData":                base64.StdEncoding.EncodeToString([]byte(req.UserData)),
		"SystemDisk.Category":     req.SystemDiskType,
		"SystemDisk.Size":         strconv.Itoa(req.SystemDiskSizeGB),
		"InternetMaxBandwidthOut": strconv.Itoa(req.InternetBandwidthMbps),
	}
	if req.Zone != "" {
		values["ZoneId"] = req.Zone
	}
	if req.VSwitchID != "" {
		values["VSwitchId"] = req.VSwitchID
	}
	if req.SSHKeyName != "" {
		values["KeyPairName"] = req.SSHKeyName
	}
	request, err := newAlibabaSignedRequest(ctx, c.config.Endpoint, accessKeyID, accessKeySecret, values)
	if err != nil {
		return VMInstance{}, err
	}
	var out struct {
		InstanceID  string `json:"InstanceId"`
		InstanceIDs struct {
			IDs []string `json:"InstanceIdSet"`
		} `json:"InstanceIdSets"`
	}
	if err := c.do(request, &out); err != nil {
		return VMInstance{}, err
	}
	instanceID := out.InstanceID
	if instanceID == "" && len(out.InstanceIDs.IDs) > 0 {
		instanceID = out.InstanceIDs.IDs[0]
	}
	if instanceID == "" {
		return VMInstance{}, &app.ProviderError{Kind: app.ProviderErrorInternal, Message: "Alibaba Cloud RunInstances returned no instance ID"}
	}
	return VMInstance{ID: instanceID, State: VMStatePending, NativeState: "Pending", Region: req.Region, Zone: req.Zone}, nil
}

func (c *alibabaClient) GetVM(ctx context.Context, id string) (VMInstance, error) {
	accessKeyID, accessKeySecret, err := c.credentials()
	if err != nil {
		return VMInstance{}, err
	}
	request, err := newAlibabaSignedRequest(ctx, c.config.Endpoint, accessKeyID, accessKeySecret, map[string]string{
		"Action":      "DescribeInstances",
		"Version":     "2014-05-26",
		"RegionId":    c.config.RegionID,
		"InstanceIds": fmt.Sprintf("[\"%s\"]", id),
	})
	if err != nil {
		return VMInstance{}, err
	}
	var out struct {
		Instances struct {
			Items []struct {
				InstanceID      string `json:"InstanceId"`
				Status          string `json:"Status"`
				RegionID        string `json:"RegionId"`
				ZoneID          string `json:"ZoneId"`
				PublicIPAddress struct {
					IPAddress []string `json:"IpAddress"`
				} `json:"PublicIpAddress"`
				VPCAttributes struct {
					PrivateIPAddress struct {
						IPAddress []string `json:"IpAddress"`
					} `json:"PrivateIpAddress"`
				} `json:"VpcAttributes"`
			} `json:"Instance"`
		} `json:"Instances"`
	}
	if err := c.do(request, &out); err != nil {
		return VMInstance{}, err
	}
	if len(out.Instances.Items) == 0 {
		return VMInstance{}, &app.ProviderError{Kind: app.ProviderErrorInvalidSpec, Message: fmt.Sprintf("Alibaba Cloud instance %q was not found", id)}
	}
	item := out.Instances.Items[0]
	return VMInstance{
		ID:          item.InstanceID,
		State:       alibabaVMState(item.Status),
		NativeState: item.Status,
		PublicIP:    firstStringSlice(item.PublicIPAddress.IPAddress),
		PrivateIP:   firstStringSlice(item.VPCAttributes.PrivateIPAddress.IPAddress),
		Region:      item.RegionID,
		Zone:        item.ZoneID,
	}, nil
}

func (c *alibabaClient) TerminateVM(ctx context.Context, id string) error {
	accessKeyID, accessKeySecret, err := c.credentials()
	if err != nil {
		return err
	}
	request, err := newAlibabaSignedRequest(ctx, c.config.Endpoint, accessKeyID, accessKeySecret, map[string]string{
		"Action":     "DeleteInstance",
		"Version":    "2014-05-26",
		"RegionId":   c.config.RegionID,
		"InstanceId": id,
		"Force":      "true",
	})
	if err != nil {
		return err
	}
	return c.do(request, nil)
}

func (c *alibabaClient) credentials() (string, string, error) {
	accessKeyID := strings.TrimSpace(c.config.AccessKeyID)
	accessKeySecret := strings.TrimSpace(c.config.AccessKeySecret)
	if accessKeyID == "" {
		if secret, err := c.config.Credentials.Resolve(credentials.Query{Provider: "alibaba-cloud", Name: "access_key_id", Env: []string{"ALIBABA_CLOUD_ACCESS_KEY_ID"}}); err == nil {
			accessKeyID = secret.Value
		}
	}
	if accessKeySecret == "" {
		if secret, err := c.config.Credentials.Resolve(credentials.Query{Provider: "alibaba-cloud", Name: "access_key_secret", Env: []string{"ALIBABA_CLOUD_ACCESS_KEY_SECRET"}}); err == nil {
			accessKeySecret = secret.Value
		}
	}
	if accessKeyID == "" || accessKeySecret == "" {
		return "", "", &app.ProviderError{Kind: app.ProviderErrorAuth, Message: "Alibaba Cloud access_key_id and access_key_secret credentials are required"}
	}
	return accessKeyID, accessKeySecret, nil
}

func newAlibabaSignedRequest(ctx context.Context, endpoint string, accessKeyID string, accessKeySecret string, values map[string]string) (*http.Request, error) {
	requestURL, err := url.Parse(endpoint)
	if err != nil {
		return nil, &app.ProviderError{Kind: app.ProviderErrorInvalidSpec, Message: fmt.Sprintf("Alibaba Cloud endpoint is invalid: %v", err), Err: err}
	}
	signedValues := map[string]string{
		"AccessKeyId":      accessKeyID,
		"Format":           "JSON",
		"SignatureMethod":  "HMAC-SHA1",
		"SignatureNonce":   strconv.FormatInt(time.Now().UnixNano(), 10),
		"SignatureVersion": "1.0",
		"Timestamp":        time.Now().UTC().Format("2006-01-02T15:04:05Z"),
	}
	for key, value := range values {
		if strings.TrimSpace(value) == "" {
			continue
		}
		signedValues[key] = value
	}
	canonicalQuery := alibabaCanonicalQuery(signedValues)
	stringToSign := "GET&%2F&" + alibabaPercentEncode(canonicalQuery)
	signature := base64.StdEncoding.EncodeToString(hmacDigest(sha1.New, []byte(accessKeySecret+"&"), []byte(stringToSign)))
	query := requestURL.Query()
	for key, value := range signedValues {
		query.Set(key, value)
	}
	query.Set("Signature", signature)
	requestURL.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL.String(), nil)
	if err != nil {
		return nil, &app.ProviderError{Kind: app.ProviderErrorInvalidSpec, Message: fmt.Sprintf("create Alibaba Cloud request: %v", err), Err: err}
	}
	return req, nil
}

func (c *alibabaClient) do(req *http.Request, out interface{}) error {
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return &app.ProviderError{Kind: app.ProviderErrorNetwork, Message: fmt.Sprintf("Alibaba Cloud request failed: %v", err), Err: err}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024))
	if err := alibabaResponseError(resp.StatusCode, body); err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(body, out); err != nil {
		return &app.ProviderError{Kind: app.ProviderErrorInternal, Message: fmt.Sprintf("decode Alibaba Cloud response: %v", err), Err: err}
	}
	return nil
}

func alibabaResponseError(statusCode int, body []byte) error {
	var parsed struct {
		Code    string `json:"Code"`
		Message string `json:"Message"`
	}
	_ = json.Unmarshal(body, &parsed)
	if statusCode >= 200 && statusCode < 300 && parsed.Code == "" {
		return nil
	}
	message := parsed.Message
	if message == "" {
		message = strings.TrimSpace(string(body))
	}
	if message == "" {
		message = fmt.Sprintf("HTTP %d", statusCode)
	}
	return &app.ProviderError{Kind: alibabaErrorKind(statusCode, parsed.Code, message), Message: "Alibaba Cloud API: " + message}
}

func alibabaErrorKind(statusCode int, code string, message string) app.ProviderErrorKind {
	lower := strings.ToLower(code + " " + message)
	switch {
	case statusCode == http.StatusUnauthorized || statusCode == http.StatusForbidden || strings.Contains(lower, "auth") || strings.Contains(lower, "accesskey") || strings.Contains(lower, "signature"):
		return app.ProviderErrorAuth
	case strings.Contains(lower, "quota"):
		return app.ProviderErrorQuota
	case strings.Contains(lower, "insufficient") || strings.Contains(lower, "notonsale") || strings.Contains(lower, "not on sale"):
		return app.ProviderErrorCapacity
	case statusCode == http.StatusBadRequest || strings.Contains(lower, "invalid"):
		return app.ProviderErrorInvalidSpec
	case statusCode >= 500:
		return app.ProviderErrorInternal
	default:
		return app.ProviderErrorUnknown
	}
}

func alibabaVMState(status string) VMState {
	switch strings.ToLower(status) {
	case "pending", "starting":
		return VMStatePending
	case "running":
		return VMStateRunning
	case "stopping":
		return VMStateTerminating
	case "stopped", "deleted":
		return VMStateTerminated
	case "error":
		return VMStateFailed
	default:
		return VMStateUnknown
	}
}

func firstStringSlice(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}
