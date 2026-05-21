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

const BaiduBCCDefaultEndpoint = "https://bcc.bj.baidubce.com"

type BaiduVMClient struct {
	Endpoint        string
	AccessKeyID     string
	SecretAccessKey string
	Credentials     credentials.Resolver
	Client          *http.Client
	Now             func() time.Time
}

type BaiduVMCreateRequest struct {
	ClientToken         string
	ZoneName            string
	ImageID             string
	InstanceSpec        string
	Name                string
	Hostname            string
	SubnetID            string
	VPCID               string
	SecurityGroupID     string
	KeyPairID           string
	RootDiskSizeGB      int
	RootDiskStorageType string
	NetworkCapacityMbps int
	UserDataBase64      string
	Tags                map[string]string
}

type BaiduVMCreateResult struct {
	InstanceID string
	RequestID  string
}

type BaiduVMStatus struct {
	InstanceID string
	State      string
	PublicIP   string
	PrivateIP  string
	RequestID  string
}

var _ VMClient = BaiduVMClient{}

func (c BaiduVMClient) ValidateAuth(ctx context.Context) error {
	httpReq, err := c.newRequest(ctx, http.MethodGet, "/v2/instance", nil, nil)
	if err != nil {
		return err
	}
	return c.do(httpReq, nil)
}

func (c BaiduVMClient) CreateVM(ctx context.Context, req VMCreateRequest) (VMInstance, error) {
	created, err := c.CreateInstance(ctx, BaiduVMCreateRequest{
		ClientToken:         clientTokenFromRun(req.RunID),
		ZoneName:            req.Zone,
		ImageID:             req.ImageID,
		InstanceSpec:        req.InstanceType,
		Name:                req.Name,
		Hostname:            req.Name,
		SubnetID:            req.SubnetID,
		VPCID:               req.NetworkID,
		SecurityGroupID:     req.SecurityGroupID,
		KeyPairID:           req.SSHKeyName,
		RootDiskSizeGB:      req.SystemDiskSizeGB,
		RootDiskStorageType: req.SystemDiskType,
		NetworkCapacityMbps: req.InternetBandwidthMbps,
		UserDataBase64:      base64.StdEncoding.EncodeToString([]byte(req.UserData)),
		Tags:                map[string]string{"switchboard": "true", "switchboard-run-id": req.RunID},
	})
	if err != nil {
		return VMInstance{}, err
	}
	return VMInstance{
		ID:          created.InstanceID,
		State:       VMStatePending,
		NativeState: "Creating",
		Region:      req.Region,
		Zone:        req.Zone,
	}, nil
}

func (c BaiduVMClient) GetVM(ctx context.Context, id string) (VMInstance, error) {
	status, err := c.DescribeInstance(ctx, id)
	if err != nil {
		return VMInstance{}, err
	}
	return VMInstance{
		ID:          status.InstanceID,
		State:       baiduVMState(status.State),
		NativeState: status.State,
		PublicIP:    status.PublicIP,
		PrivateIP:   status.PrivateIP,
	}, nil
}

func (c BaiduVMClient) TerminateVM(ctx context.Context, id string) error {
	return c.TerminateInstance(ctx, id)
}

func (c BaiduVMClient) CreateInstance(ctx context.Context, req BaiduVMCreateRequest) (BaiduVMCreateResult, error) {
	query := url.Values{}
	query.Set("clientToken", req.ClientToken)
	body, err := json.Marshal(baiduCreatePayload(req))
	if err != nil {
		return BaiduVMCreateResult{}, err
	}
	httpReq, err := c.newRequest(ctx, http.MethodPost, "/v2/instanceBySpec", query, body)
	if err != nil {
		return BaiduVMCreateResult{}, err
	}
	var out struct {
		InstanceIDs []string `json:"instanceIds"`
		InstanceID  string   `json:"instanceId"`
	}
	if err := c.do(httpReq, &out); err != nil {
		return BaiduVMCreateResult{}, err
	}
	instanceID := out.InstanceID
	if instanceID == "" && len(out.InstanceIDs) > 0 {
		instanceID = out.InstanceIDs[0]
	}
	if instanceID == "" {
		return BaiduVMCreateResult{}, &app.ProviderError{Kind: app.ProviderErrorInternal, Message: "Baidu AI Cloud BCC create returned no instance IDs"}
	}
	return BaiduVMCreateResult{InstanceID: instanceID}, nil
}

func (c BaiduVMClient) DescribeInstance(ctx context.Context, instanceID string) (BaiduVMStatus, error) {
	httpReq, err := c.newRequest(ctx, http.MethodGet, "/v2/instance/"+url.PathEscape(instanceID), nil, nil)
	if err != nil {
		return BaiduVMStatus{}, err
	}
	var out struct {
		Instance struct {
			ID         string `json:"id"`
			Status     string `json:"status"`
			PublicIP   string `json:"publicIp"`
			InternalIP string `json:"internalIp"`
		} `json:"instance"`
	}
	if err := c.do(httpReq, &out); err != nil {
		return BaiduVMStatus{}, err
	}
	if out.Instance.ID == "" {
		return BaiduVMStatus{}, &app.ProviderError{Kind: app.ProviderErrorUnknown, Message: fmt.Sprintf("Baidu AI Cloud BCC instance %q was not found", instanceID)}
	}
	return BaiduVMStatus{
		InstanceID: out.Instance.ID,
		State:      out.Instance.Status,
		PublicIP:   out.Instance.PublicIP,
		PrivateIP:  out.Instance.InternalIP,
	}, nil
}

func (c BaiduVMClient) TerminateInstance(ctx context.Context, instanceID string) error {
	body, err := json.Marshal(map[string]bool{
		"relatedReleaseFlag":    true,
		"deleteCdsSnapshotFlag": false,
		"deleteRelatedEnisFlag": true,
		"bccRecycleFlag":        true,
	})
	if err != nil {
		return err
	}
	httpReq, err := c.newRequest(ctx, http.MethodPost, "/v2/instance/"+url.PathEscape(instanceID), nil, body)
	if err != nil {
		return err
	}
	return c.do(httpReq, nil)
}

func (c BaiduVMClient) newRequest(ctx context.Context, method string, path string, query url.Values, body []byte) (*http.Request, error) {
	accessKeyID, secretAccessKey, err := c.credentials()
	if err != nil {
		return nil, err
	}
	endpoint := strings.TrimRight(c.Endpoint, "/")
	if endpoint == "" {
		endpoint = BaiduBCCDefaultEndpoint
	}
	requestURL, err := url.Parse(endpoint + path)
	if err != nil {
		return nil, &app.ProviderError{Kind: app.ProviderErrorInvalidSpec, Message: fmt.Sprintf("Baidu AI Cloud BCC endpoint is invalid: %v", err), Err: err}
	}
	requestURL.RawQuery = query.Encode()
	now := time.Now().UTC()
	if c.Now != nil {
		now = c.Now().UTC()
	}
	date := now.Format("2006-01-02T15:04:05Z")
	expirationSeconds := "1800"
	signedHeaders := "content-type;host;x-bce-date"
	authPrefix := fmt.Sprintf("bce-auth-v1/%s/%s/%s", accessKeyID, date, expirationSeconds)
	signingKey := fmt.Sprintf("%x", hmacDigest(sha256.New, []byte(secretAccessKey), []byte(authPrefix)))
	contentType := "application/json"
	canonicalHeaders := fmt.Sprintf("content-type:%s\nhost:%s\nx-bce-date:%s", contentType, requestURL.Host, date)
	canonicalRequest := strings.Join([]string{
		method,
		canonicalPath(requestURL.EscapedPath()),
		canonicalQueryString(requestURL.Query()),
		canonicalHeaders,
	}, "\n")
	signature := fmt.Sprintf("%x", hmacDigest(sha256.New, []byte(signingKey), []byte(canonicalRequest)))
	authorization := fmt.Sprintf("%s/%s/%s", authPrefix, signedHeaders, signature)

	req, err := http.NewRequestWithContext(ctx, method, requestURL.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Host = requestURL.Host
	req.Header.Set("Authorization", authorization)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("x-bce-date", date)
	return req, nil
}

func (c BaiduVMClient) do(req *http.Request, out interface{}) error {
	client := c.Client
	if client == nil {
		client = &http.Client{Timeout: defaultAuthTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return &app.ProviderError{Kind: app.ProviderErrorNetwork, Message: fmt.Sprintf("Baidu AI Cloud BCC request failed: %v", err), Err: err}
	}
	return decodeProviderJSON("Baidu AI Cloud BCC", resp, out)
}

func (c BaiduVMClient) credentials() (string, string, error) {
	accessKeyID := strings.TrimSpace(c.AccessKeyID)
	secretAccessKey := strings.TrimSpace(c.SecretAccessKey)
	if accessKeyID == "" {
		if secret, err := c.Credentials.Resolve(credentials.Query{Provider: "baidu-ai-cloud", Name: "access_key_id", Env: []string{"BAIDU_CLOUD_ACCESS_KEY_ID", "BCE_ACCESS_KEY_ID"}}); err == nil {
			accessKeyID = secret.Value
		}
	}
	if secretAccessKey == "" {
		if secret, err := c.Credentials.Resolve(credentials.Query{Provider: "baidu-ai-cloud", Name: "secret_access_key", Env: []string{"BAIDU_CLOUD_SECRET_ACCESS_KEY", "BCE_SECRET_ACCESS_KEY"}}); err == nil {
			secretAccessKey = secret.Value
		}
	}
	if accessKeyID == "" || secretAccessKey == "" {
		return "", "", &app.ProviderError{Kind: app.ProviderErrorAuth, Message: "Baidu AI Cloud access_key_id and secret_access_key credentials are required"}
	}
	return accessKeyID, secretAccessKey, nil
}

func baiduCreatePayload(req BaiduVMCreateRequest) map[string]interface{} {
	payload := map[string]interface{}{
		"spec":          req.InstanceSpec,
		"imageId":       req.ImageID,
		"name":          req.Name,
		"hostname":      req.Hostname,
		"zoneName":      req.ZoneName,
		"subnetId":      req.SubnetID,
		"vpcId":         req.VPCID,
		"keypairId":     req.KeyPairID,
		"paymentTiming": "Postpaid",
	}
	if req.SecurityGroupID != "" {
		payload["securityGroupId"] = req.SecurityGroupID
	}
	if req.RootDiskSizeGB > 0 {
		payload["rootDiskSizeInGb"] = req.RootDiskSizeGB
	}
	if req.RootDiskStorageType != "" {
		payload["rootDiskStorageType"] = req.RootDiskStorageType
	}
	if req.NetworkCapacityMbps > 0 {
		payload["networkCapacityInMbps"] = req.NetworkCapacityMbps
	}
	if req.UserDataBase64 != "" {
		payload["userData"] = req.UserDataBase64
	}
	if len(req.Tags) > 0 {
		payload["tags"] = baiduTags(req.Tags)
	}
	return payload
}

func baiduTags(tags map[string]string) []map[string]string {
	keys := make([]string, 0, len(tags))
	for key := range tags {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]map[string]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, map[string]string{"tagKey": key, "tagValue": tags[key]})
	}
	return out
}

func baiduVMState(state string) VMState {
	switch strings.ToLower(state) {
	case "creating", "starting", "pending", "rebooting":
		return VMStatePending
	case "running":
		return VMStateRunning
	case "stopping", "deleting":
		return VMStateTerminating
	case "stopped", "deleted", "terminated":
		return VMStateTerminated
	case "error", "failed":
		return VMStateFailed
	default:
		return VMStateUnknown
	}
}

func clientTokenFromRun(runID string) string {
	if runID == "" {
		return strconv.FormatInt(time.Now().UnixNano(), 10)
	}
	return runID
}
