package chinacloud

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/anthonylu23/switchboard-cli/internal/app"
	"github.com/anthonylu23/switchboard-cli/internal/credentials"
)

const (
	TencentCVMDefaultEndpoint = "https://cvm.tencentcloudapi.com/"
	tencentCVMVersion         = "2017-03-12"
)

type TencentVMClient struct {
	Endpoint    string
	Region      string
	SecretID    string
	SecretKey   string
	Credentials credentials.Resolver
	Client      *http.Client
	Now         func() time.Time
}

type TencentVMCreateRequest struct {
	Zone                    string
	ImageID                 string
	InstanceType            string
	InstanceName            string
	KeyPairID               string
	VPCID                   string
	SubnetID                string
	SecurityGroupIDs        []string
	SystemDiskType          string
	SystemDiskSizeGB        int
	InternetMaxBandwidthOut int
	PublicIPAssigned        bool
	UserDataBase64          string
	ClientToken             string
	Tags                    map[string]string
}

type TencentVMCreateResult struct {
	InstanceID string
	RequestID  string
}

type TencentVMStatus struct {
	InstanceID string
	State      string
	PublicIP   string
	PrivateIP  string
	RequestID  string
}

var _ VMClient = TencentVMClient{}

func (c TencentVMClient) ValidateAuth(ctx context.Context) error {
	body := []byte("{}")
	httpReq, err := c.newRequest(ctx, "DescribeRegions", body)
	if err != nil {
		return err
	}
	var out struct {
		Response struct {
			Error *struct {
				Code    string `json:"Code"`
				Message string `json:"Message"`
			} `json:"Error"`
		} `json:"Response"`
	}
	if err := c.do(httpReq, &out); err != nil {
		return err
	}
	if out.Response.Error != nil {
		return tencentAPIError(out.Response.Error.Code, out.Response.Error.Message)
	}
	return nil
}

func (c TencentVMClient) CreateVM(ctx context.Context, req VMCreateRequest) (VMInstance, error) {
	securityGroups := []string{}
	if req.SecurityGroupID != "" {
		securityGroups = append(securityGroups, req.SecurityGroupID)
	}
	created, err := c.CreateInstance(ctx, TencentVMCreateRequest{
		Zone:                    req.Zone,
		ImageID:                 req.ImageID,
		InstanceType:            req.InstanceType,
		InstanceName:            req.Name,
		KeyPairID:               req.SSHKeyName,
		VPCID:                   req.NetworkID,
		SubnetID:                req.SubnetID,
		SecurityGroupIDs:        securityGroups,
		SystemDiskType:          req.SystemDiskType,
		SystemDiskSizeGB:        req.SystemDiskSizeGB,
		InternetMaxBandwidthOut: req.InternetBandwidthMbps,
		PublicIPAssigned:        req.InternetBandwidthMbps > 0,
		UserDataBase64:          base64.StdEncoding.EncodeToString([]byte(req.UserData)),
		ClientToken:             req.RunID,
		Tags:                    map[string]string{"switchboard": "true", "switchboard-run-id": req.RunID},
	})
	if err != nil {
		return VMInstance{}, err
	}
	return VMInstance{
		ID:          created.InstanceID,
		State:       VMStatePending,
		NativeState: "PENDING",
		Region:      req.Region,
		Zone:        req.Zone,
	}, nil
}

func (c TencentVMClient) GetVM(ctx context.Context, id string) (VMInstance, error) {
	status, err := c.DescribeInstance(ctx, id)
	if err != nil {
		return VMInstance{}, err
	}
	return VMInstance{
		ID:          status.InstanceID,
		State:       tencentVMState(status.State),
		NativeState: status.State,
		PublicIP:    status.PublicIP,
		PrivateIP:   status.PrivateIP,
		Region:      c.Region,
	}, nil
}

func (c TencentVMClient) TerminateVM(ctx context.Context, id string) error {
	return c.TerminateInstance(ctx, id)
}

func (c TencentVMClient) CreateInstance(ctx context.Context, req TencentVMCreateRequest) (TencentVMCreateResult, error) {
	body, err := json.Marshal(tencentRunInstancesPayload(req))
	if err != nil {
		return TencentVMCreateResult{}, err
	}
	httpReq, err := c.newRequest(ctx, "RunInstances", body)
	if err != nil {
		return TencentVMCreateResult{}, err
	}
	var out struct {
		Response struct {
			InstanceIDSet []string `json:"InstanceIdSet"`
			RequestID     string   `json:"RequestId"`
			Error         *struct {
				Code    string `json:"Code"`
				Message string `json:"Message"`
			} `json:"Error"`
		} `json:"Response"`
	}
	if err := c.do(httpReq, &out); err != nil {
		return TencentVMCreateResult{}, err
	}
	if out.Response.Error != nil {
		return TencentVMCreateResult{}, tencentAPIError(out.Response.Error.Code, out.Response.Error.Message)
	}
	if len(out.Response.InstanceIDSet) == 0 {
		return TencentVMCreateResult{}, &app.ProviderError{Kind: app.ProviderErrorInternal, Message: "Tencent Cloud RunInstances returned no instance IDs"}
	}
	return TencentVMCreateResult{InstanceID: out.Response.InstanceIDSet[0], RequestID: out.Response.RequestID}, nil
}

func (c TencentVMClient) DescribeInstance(ctx context.Context, instanceID string) (TencentVMStatus, error) {
	body, err := json.Marshal(map[string]interface{}{"InstanceIds": []string{instanceID}})
	if err != nil {
		return TencentVMStatus{}, err
	}
	httpReq, err := c.newRequest(ctx, "DescribeInstances", body)
	if err != nil {
		return TencentVMStatus{}, err
	}
	var out struct {
		Response struct {
			InstanceSet []struct {
				InstanceID        string   `json:"InstanceId"`
				InstanceState     string   `json:"InstanceState"`
				PublicIPAddresses []string `json:"PublicIpAddresses"`
				PrivateIPAddress  string   `json:"PrivateIpAddress"`
			} `json:"InstanceSet"`
			RequestID string `json:"RequestId"`
			Error     *struct {
				Code    string `json:"Code"`
				Message string `json:"Message"`
			} `json:"Error"`
		} `json:"Response"`
	}
	if err := c.do(httpReq, &out); err != nil {
		return TencentVMStatus{}, err
	}
	if out.Response.Error != nil {
		return TencentVMStatus{}, tencentAPIError(out.Response.Error.Code, out.Response.Error.Message)
	}
	if len(out.Response.InstanceSet) == 0 {
		return TencentVMStatus{}, &app.ProviderError{Kind: app.ProviderErrorUnknown, Message: fmt.Sprintf("Tencent Cloud instance %q was not found", instanceID)}
	}
	instance := out.Response.InstanceSet[0]
	status := TencentVMStatus{
		InstanceID: instance.InstanceID,
		State:      instance.InstanceState,
		PrivateIP:  instance.PrivateIPAddress,
		RequestID:  out.Response.RequestID,
	}
	if len(instance.PublicIPAddresses) > 0 {
		status.PublicIP = instance.PublicIPAddresses[0]
	}
	return status, nil
}

func (c TencentVMClient) TerminateInstance(ctx context.Context, instanceID string) error {
	body, err := json.Marshal(map[string]interface{}{"InstanceIds": []string{instanceID}})
	if err != nil {
		return err
	}
	httpReq, err := c.newRequest(ctx, "TerminateInstances", body)
	if err != nil {
		return err
	}
	var out struct {
		Response struct {
			RequestID string `json:"RequestId"`
			Error     *struct {
				Code    string `json:"Code"`
				Message string `json:"Message"`
			} `json:"Error"`
		} `json:"Response"`
	}
	if err := c.do(httpReq, &out); err != nil {
		return err
	}
	if out.Response.Error != nil {
		return tencentAPIError(out.Response.Error.Code, out.Response.Error.Message)
	}
	return nil
}

func (c TencentVMClient) newRequest(ctx context.Context, action string, body []byte) (*http.Request, error) {
	secretID, secretKey, err := c.credentials()
	if err != nil {
		return nil, err
	}
	endpoint := c.Endpoint
	if endpoint == "" {
		endpoint = TencentCVMDefaultEndpoint
	}
	region := c.Region
	if region == "" {
		region = "ap-guangzhou"
	}
	parsed, err := url.Parse(endpoint)
	if err != nil {
		return nil, &app.ProviderError{Kind: app.ProviderErrorInvalidSpec, Message: fmt.Sprintf("Tencent Cloud endpoint is invalid: %v", err), Err: err}
	}
	if parsed.Path == "" {
		parsed.Path = "/"
	}
	now := time.Now().UTC()
	if c.Now != nil {
		now = c.Now().UTC()
	}
	timestamp := strconv.FormatInt(now.Unix(), 10)
	date := now.Format("2006-01-02")
	contentType := "application/json; charset=utf-8"
	service := "cvm"
	canonicalHeaders := fmt.Sprintf("content-type:%s\nhost:%s\nx-tc-action:%s\n", contentType, parsed.Host, strings.ToLower(action))
	signedHeaders := "content-type;host;x-tc-action"
	canonicalRequest := strings.Join([]string{
		http.MethodPost,
		"/",
		"",
		canonicalHeaders,
		signedHeaders,
		sha256Hex(body),
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
	signature := fmt.Sprintf("%x", hmacDigest(sha256.New, secretSigning, []byte(stringToSign)))
	authorization := fmt.Sprintf("TC3-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s", secretID, credentialScope, signedHeaders, signature)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, parsed.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Host = parsed.Host
	req.Header.Set("Authorization", authorization)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("X-TC-Action", action)
	req.Header.Set("X-TC-Timestamp", timestamp)
	req.Header.Set("X-TC-Version", tencentCVMVersion)
	req.Header.Set("X-TC-Region", region)
	return req, nil
}

func (c TencentVMClient) do(req *http.Request, out interface{}) error {
	client := c.Client
	if client == nil {
		client = &http.Client{Timeout: defaultAuthTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return &app.ProviderError{Kind: app.ProviderErrorNetwork, Message: fmt.Sprintf("Tencent Cloud CVM request failed: %v", err), Err: err}
	}
	return decodeProviderJSON("Tencent Cloud CVM", resp, out)
}

func (c TencentVMClient) credentials() (string, string, error) {
	secretID := strings.TrimSpace(c.SecretID)
	secretKey := strings.TrimSpace(c.SecretKey)
	if secretID == "" {
		if secret, err := c.Credentials.Resolve(credentials.Query{Provider: "tencent-cloud", Name: "secret_id", Env: []string{"TENCENTCLOUD_SECRET_ID", "TENCENT_CLOUD_SECRET_ID"}}); err == nil {
			secretID = secret.Value
		}
	}
	if secretKey == "" {
		if secret, err := c.Credentials.Resolve(credentials.Query{Provider: "tencent-cloud", Name: "secret_key", Env: []string{"TENCENTCLOUD_SECRET_KEY", "TENCENT_CLOUD_SECRET_KEY"}}); err == nil {
			secretKey = secret.Value
		}
	}
	if secretID == "" || secretKey == "" {
		return "", "", &app.ProviderError{Kind: app.ProviderErrorAuth, Message: "Tencent Cloud secret_id and secret_key credentials are required"}
	}
	return secretID, secretKey, nil
}

func tencentRunInstancesPayload(req TencentVMCreateRequest) map[string]interface{} {
	payload := map[string]interface{}{
		"InstanceChargeType": "POSTPAID_BY_HOUR",
		"Placement": map[string]interface{}{
			"Zone": req.Zone,
		},
		"ImageId":       req.ImageID,
		"InstanceType":  req.InstanceType,
		"InstanceName":  req.InstanceName,
		"InstanceCount": 1,
	}
	if req.ClientToken != "" {
		payload["ClientToken"] = req.ClientToken
	}
	if req.SystemDiskType != "" || req.SystemDiskSizeGB > 0 {
		systemDisk := map[string]interface{}{}
		if req.SystemDiskType != "" {
			systemDisk["DiskType"] = req.SystemDiskType
		}
		if req.SystemDiskSizeGB > 0 {
			systemDisk["DiskSize"] = req.SystemDiskSizeGB
		}
		payload["SystemDisk"] = systemDisk
	}
	if req.VPCID != "" || req.SubnetID != "" {
		payload["VirtualPrivateCloud"] = map[string]interface{}{
			"VpcId":    req.VPCID,
			"SubnetId": req.SubnetID,
		}
	}
	if len(req.SecurityGroupIDs) > 0 {
		payload["SecurityGroupIds"] = append([]string(nil), req.SecurityGroupIDs...)
	}
	if req.KeyPairID != "" {
		payload["LoginSettings"] = map[string]interface{}{"KeyIds": []string{req.KeyPairID}}
	}
	if req.PublicIPAssigned || req.InternetMaxBandwidthOut > 0 {
		payload["InternetAccessible"] = map[string]interface{}{
			"InternetChargeType":      "TRAFFIC_POSTPAID_BY_HOUR",
			"InternetMaxBandwidthOut": req.InternetMaxBandwidthOut,
			"PublicIpAssigned":        req.PublicIPAssigned,
		}
	}
	if req.UserDataBase64 != "" {
		payload["UserData"] = req.UserDataBase64
	}
	if len(req.Tags) > 0 {
		payload["TagSpecification"] = []map[string]interface{}{
			{
				"ResourceType": "instance",
				"Tags":         tencentTags(req.Tags),
			},
		}
	}
	return payload
}

func tencentTags(tags map[string]string) []map[string]string {
	keys := make([]string, 0, len(tags))
	for key := range tags {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]map[string]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, map[string]string{"Key": key, "Value": tags[key]})
	}
	return out
}

func tencentVMState(state string) VMState {
	switch strings.ToLower(state) {
	case "pending", "starting", "rebooting":
		return VMStatePending
	case "running":
		return VMStateRunning
	case "stopping":
		return VMStateTerminating
	case "stopped", "shutdown", "terminated":
		return VMStateTerminated
	case "launch_failed", "failed", "error":
		return VMStateFailed
	default:
		return VMStateUnknown
	}
}

func tencentAPIError(code string, message string) error {
	kind := app.ProviderErrorUnknown
	lower := strings.ToLower(code + " " + message)
	switch {
	case strings.Contains(lower, "auth") || strings.Contains(lower, "unauthorized") || strings.Contains(lower, "secret"):
		kind = app.ProviderErrorAuth
	case strings.Contains(lower, "quota"):
		kind = app.ProviderErrorQuota
	case strings.Contains(lower, "resourceinsufficient") || strings.Contains(lower, "capacity"):
		kind = app.ProviderErrorCapacity
	case strings.Contains(lower, "invalid") || strings.Contains(lower, "unsupported"):
		kind = app.ProviderErrorInvalidSpec
	}
	return &app.ProviderError{Kind: kind, Message: fmt.Sprintf("Tencent Cloud CVM error %s: %s", code, message)}
}
