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
	"strings"
	"time"

	"github.com/anthonylu23/switchboard-cli/internal/app"
	"github.com/anthonylu23/switchboard-cli/internal/credentials"
)

const (
	TianyiECSDefaultEndpoint = "https://ctecs-global.ctapi.ctyun.cn"
	TianyiCreateECSPath      = "/v4/dec/ecs-create"
	TianyiDescribeECSPath    = "/v4/ecs/details"
	TianyiTerminateECSPath   = "/v4/ecs/delete"
)

type TianyiVMClient struct {
	Endpoint    string
	RegionID    string
	AZName      string
	AccessKey   string
	SecretKey   string
	Credentials credentials.Resolver
	Client      *http.Client
	Now         func() time.Time
	RequestID   func() string
}

type TianyiVMCreateRequest struct {
	ClientToken      string
	RegionID         string
	AZName           string
	DisplayName      string
	HostName         string
	FlavorID         string
	ImageID          string
	ImagePublic      int
	SystemDiskType   string
	SystemDiskSizeGB int
	VPCID            string
	SubnetID         string
	SecurityGroupIDs []string
	PublicIP         bool
	UserDataBase64   string
	Metadata         map[string]string
}

type TianyiVMCreateResult struct {
	InstanceID string
	OrderID    string
	RequestID  string
}

type TianyiVMStatus struct {
	InstanceID string
	State      string
	PublicIP   string
	PrivateIP  string
	RequestID  string
}

var _ VMClient = TianyiVMClient{}

func (c TianyiVMClient) ValidateAuth(ctx context.Context) error {
	httpReq, err := c.newRequest(ctx, http.MethodGet, "/v3/cluster/describeRegionClusters", nil, nil)
	if err != nil {
		return err
	}
	return c.do(httpReq, nil)
}

func (c TianyiVMClient) CreateVM(ctx context.Context, req VMCreateRequest) (VMInstance, error) {
	imagePublic := 1
	created, err := c.CreateInstance(ctx, TianyiVMCreateRequest{
		ClientToken:      req.RunID,
		RegionID:         req.Region,
		AZName:           req.Zone,
		DisplayName:      req.Name,
		HostName:         req.Name,
		FlavorID:         req.InstanceType,
		ImageID:          req.ImageID,
		ImagePublic:      imagePublic,
		SystemDiskType:   req.SystemDiskType,
		SystemDiskSizeGB: req.SystemDiskSizeGB,
		VPCID:            req.NetworkID,
		SubnetID:         req.SubnetID,
		SecurityGroupIDs: securityGroupList(req.SecurityGroupID),
		PublicIP:         req.InternetBandwidthMbps > 0,
		UserDataBase64:   base64.StdEncoding.EncodeToString([]byte(req.UserData)),
		Metadata:         map[string]string{"switchboard": "true", "switchboard-run-id": req.RunID},
	})
	if err != nil {
		return VMInstance{}, err
	}
	if created.InstanceID == "" {
		return VMInstance{}, &app.ProviderError{Kind: app.ProviderErrorInternal, Message: fmt.Sprintf("Tianyi Cloud ECS create returned no instance ID for order %q", created.OrderID)}
	}
	return VMInstance{
		ID:          created.InstanceID,
		State:       VMStatePending,
		NativeState: "PENDING",
		Region:      req.Region,
		Zone:        req.Zone,
	}, nil
}

func (c TianyiVMClient) GetVM(ctx context.Context, id string) (VMInstance, error) {
	status, err := c.DescribeInstance(ctx, c.RegionID, c.AZName, id)
	if err != nil {
		return VMInstance{}, err
	}
	return VMInstance{
		ID:          status.InstanceID,
		State:       tianyiVMState(status.State),
		NativeState: status.State,
		PublicIP:    status.PublicIP,
		PrivateIP:   status.PrivateIP,
		Region:      c.RegionID,
		Zone:        c.AZName,
	}, nil
}

func (c TianyiVMClient) TerminateVM(ctx context.Context, id string) error {
	return c.TerminateInstance(ctx, c.RegionID, c.AZName, id)
}

func (c TianyiVMClient) CreateInstance(ctx context.Context, req TianyiVMCreateRequest) (TianyiVMCreateResult, error) {
	body, err := json.Marshal(tianyiCreatePayload(req))
	if err != nil {
		return TianyiVMCreateResult{}, err
	}
	httpReq, err := c.newRequest(ctx, http.MethodPost, TianyiCreateECSPath, nil, body)
	if err != nil {
		return TianyiVMCreateResult{}, err
	}
	var out tianyiEnvelope
	if err := c.do(httpReq, &out); err != nil {
		return TianyiVMCreateResult{}, err
	}
	if err := out.err("Tianyi Cloud ECS create failed"); err != nil {
		return TianyiVMCreateResult{}, err
	}
	return TianyiVMCreateResult{
		InstanceID: firstTianyiString(out.ReturnObj, "instanceID", "instanceId", "serverID", "serverId"),
		OrderID:    firstTianyiString(out.ReturnObj, "masterOrderID", "masterOrderId", "orderID", "orderId"),
		RequestID:  out.RequestID,
	}, nil
}

func (c TianyiVMClient) DescribeInstance(ctx context.Context, regionID string, azName string, instanceID string) (TianyiVMStatus, error) {
	query := url.Values{}
	query.Set("regionID", regionID)
	query.Set("azName", azName)
	query.Set("instanceID", instanceID)
	httpReq, err := c.newRequest(ctx, http.MethodGet, TianyiDescribeECSPath, query, nil)
	if err != nil {
		return TianyiVMStatus{}, err
	}
	var out tianyiEnvelope
	if err := c.do(httpReq, &out); err != nil {
		return TianyiVMStatus{}, err
	}
	if err := out.err("Tianyi Cloud ECS describe failed"); err != nil {
		return TianyiVMStatus{}, err
	}
	return TianyiVMStatus{
		InstanceID: firstTianyiString(out.ReturnObj, "instanceID", "instanceId"),
		State:      firstTianyiString(out.ReturnObj, "status", "state", "instanceStatus"),
		PublicIP:   firstTianyiString(out.ReturnObj, "publicIP", "publicIp", "eipAddress"),
		PrivateIP:  firstTianyiString(out.ReturnObj, "privateIP", "privateIp", "fixedIP"),
		RequestID:  out.RequestID,
	}, nil
}

func (c TianyiVMClient) TerminateInstance(ctx context.Context, regionID string, azName string, instanceID string) error {
	body, err := json.Marshal(map[string]string{
		"clientToken": requestID(),
		"regionID":    regionID,
		"azName":      azName,
		"instanceID":  instanceID,
	})
	if err != nil {
		return err
	}
	httpReq, err := c.newRequest(ctx, http.MethodPost, TianyiTerminateECSPath, nil, body)
	if err != nil {
		return err
	}
	var out tianyiEnvelope
	if err := c.do(httpReq, &out); err != nil {
		return err
	}
	return out.err("Tianyi Cloud ECS terminate failed")
}

func (c TianyiVMClient) newRequest(ctx context.Context, method string, path string, query url.Values, body []byte) (*http.Request, error) {
	accessKey, secretKey, err := c.credentials()
	if err != nil {
		return nil, err
	}
	endpoint := strings.TrimRight(c.Endpoint, "/")
	if endpoint == "" {
		endpoint = TianyiECSDefaultEndpoint
	}
	requestURL, err := url.Parse(endpoint + path)
	if err != nil {
		return nil, &app.ProviderError{Kind: app.ProviderErrorInvalidSpec, Message: fmt.Sprintf("Tianyi Cloud endpoint is invalid: %v", err), Err: err}
	}
	requestURL.RawQuery = query.Encode()
	now := time.Now().UTC()
	if c.Now != nil {
		now = c.Now().UTC()
	}
	eopDate := now.Format("20060102T150405Z")
	rid := requestID()
	if c.RequestID != nil {
		rid = c.RequestID()
	}
	bodyDigest := sha256Hex(body)
	headerString := fmt.Sprintf("ctyun-eop-request-id:%s\neop-date:%s\n", rid, eopDate)
	signatureString := fmt.Sprintf("%s\n%s\n%s", headerString, canonicalQueryString(requestURL.Query()), bodyDigest)
	datePart := strings.Split(eopDate, "T")[0]
	kTime := hmacDigest(sha256.New, []byte(secretKey), []byte(eopDate))
	kAccessKey := hmacDigest(sha256.New, kTime, []byte(accessKey))
	kDate := hmacDigest(sha256.New, kAccessKey, []byte(datePart))
	signature := base64.StdEncoding.EncodeToString(hmacDigest(sha256.New, kDate, []byte(signatureString)))
	authorization := fmt.Sprintf("%s Headers=ctyun-eop-request-id;eop-date Signature=%s", accessKey, signature)

	req, err := http.NewRequestWithContext(ctx, method, requestURL.String(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Host = requestURL.Host
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("ctyun-eop-request-id", rid)
	req.Header.Set("Eop-Authorization", authorization)
	req.Header.Set("Eop-date", eopDate)
	return req, nil
}

func (c TianyiVMClient) do(req *http.Request, out interface{}) error {
	client := c.Client
	if client == nil {
		client = &http.Client{Timeout: defaultAuthTimeout}
	}
	resp, err := client.Do(req)
	if err != nil {
		return &app.ProviderError{Kind: app.ProviderErrorNetwork, Message: fmt.Sprintf("Tianyi Cloud ECS request failed: %v", err), Err: err}
	}
	return decodeProviderJSON("Tianyi Cloud ECS", resp, out)
}

func (c TianyiVMClient) credentials() (string, string, error) {
	accessKey := strings.TrimSpace(c.AccessKey)
	secretKey := strings.TrimSpace(c.SecretKey)
	if accessKey == "" {
		if secret, err := c.Credentials.Resolve(credentials.Query{Provider: "tianyi-cloud", Name: "access_key", Env: []string{"CTYUN_ACCESS_KEY", "TIANYI_CLOUD_ACCESS_KEY"}}); err == nil {
			accessKey = secret.Value
		}
	}
	if secretKey == "" {
		if secret, err := c.Credentials.Resolve(credentials.Query{Provider: "tianyi-cloud", Name: "secret_key", Env: []string{"CTYUN_SECRET_KEY", "TIANYI_CLOUD_SECRET_KEY"}}); err == nil {
			secretKey = secret.Value
		}
	}
	if accessKey == "" || secretKey == "" {
		return "", "", &app.ProviderError{Kind: app.ProviderErrorAuth, Message: "Tianyi Cloud access_key and secret_key credentials are required"}
	}
	return accessKey, secretKey, nil
}

type tianyiEnvelope struct {
	StatusCode  int                    `json:"statusCode"`
	Code        interface{}            `json:"code"`
	Message     string                 `json:"message"`
	Description string                 `json:"description"`
	Details     string                 `json:"details"`
	RequestID   string                 `json:"requestId"`
	ReturnObj   map[string]interface{} `json:"returnObj"`
}

func (e tianyiEnvelope) err(prefix string) error {
	success := e.StatusCode == 0 || e.StatusCode == 800 || e.StatusCode == 200
	if !success {
		return &app.ProviderError{Kind: app.ProviderErrorUnknown, Message: fmt.Sprintf("%s: statusCode=%d message=%s details=%s", prefix, e.StatusCode, e.Message, e.Details)}
	}
	if strings.EqualFold(e.Message, "success") || strings.EqualFold(e.Description, "success") || e.Message == "" {
		return nil
	}
	lower := strings.ToLower(e.Message + " " + e.Description + " " + e.Details)
	if strings.Contains(lower, "fail") || strings.Contains(lower, "error") {
		return &app.ProviderError{Kind: app.ProviderErrorUnknown, Message: fmt.Sprintf("%s: %s %s", prefix, e.Message, e.Details)}
	}
	return nil
}

func tianyiCreatePayload(req TianyiVMCreateRequest) map[string]interface{} {
	payload := map[string]interface{}{
		"clientToken": req.ClientToken,
		"regionID":    req.RegionID,
		"azName":      req.AZName,
		"displayName": req.DisplayName,
		"flavorID":    req.FlavorID,
		"imagePublic": req.ImagePublic,
		"imageID":     req.ImageID,
		"syshdType":   req.SystemDiskType,
		"syshd":       req.SystemDiskSizeGB,
		"vpc":         req.VPCID,
		"extIP":       "0",
	}
	if req.HostName != "" {
		payload["name"] = req.HostName
	}
	if len(req.SecurityGroupIDs) > 0 {
		payload["secGroupList"] = append([]string(nil), req.SecurityGroupIDs...)
	}
	if req.SubnetID != "" {
		payload["networkCardList"] = []map[string]string{{"subnetID": req.SubnetID}}
	}
	if req.PublicIP {
		payload["extIP"] = "1"
	}
	if req.UserDataBase64 != "" {
		payload["userData"] = req.UserDataBase64
	}
	if len(req.Metadata) > 0 {
		payload["metadata"] = req.Metadata
	}
	return payload
}

func firstTianyiString(values map[string]interface{}, keys ...string) string {
	for _, key := range keys {
		if value, ok := values[key]; ok {
			switch typed := value.(type) {
			case string:
				return typed
			case fmt.Stringer:
				return typed.String()
			}
		}
	}
	return ""
}

func securityGroupList(securityGroupID string) []string {
	if securityGroupID == "" {
		return nil
	}
	return []string{securityGroupID}
}

func tianyiVMState(state string) VMState {
	switch strings.ToLower(state) {
	case "pending", "creating", "starting", "building":
		return VMStatePending
	case "running", "active":
		return VMStateRunning
	case "stopping", "deleting", "rebooting":
		return VMStateTerminating
	case "stopped", "deleted", "terminated":
		return VMStateTerminated
	case "error", "failed", "fault":
		return VMStateFailed
	default:
		return VMStateUnknown
	}
}
