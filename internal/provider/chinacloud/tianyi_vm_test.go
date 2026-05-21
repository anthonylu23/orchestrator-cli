package chinacloud

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTianyiVMClientLifecycleRequests(t *testing.T) {
	var paths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		paths = append(paths, r.URL.Path)
		if auth := r.Header.Get("Eop-Authorization"); !strings.HasPrefix(auth, "ak-value Headers=ctyun-eop-request-id;eop-date Signature=") || strings.Contains(auth, "secret-value") {
			t.Fatalf("Eop-Authorization = %q", auth)
		}
		if r.Header.Get("ctyun-eop-request-id") != "req-fixed" || r.Header.Get("Eop-date") == "" {
			t.Fatalf("headers = %#v", r.Header)
		}
		switch r.URL.Path {
		case TianyiCreateECSPath:
			var payload map[string]interface{}
			if err := json.Unmarshal(body, &payload); err != nil {
				t.Fatalf("payload json: %v", err)
			}
			if payload["imageID"] != "img-test" || payload["userData"] == "" || payload["extIP"] != "1" {
				t.Fatalf("create payload = %#v", payload)
			}
			_, _ = w.Write([]byte(`{"statusCode":800,"message":"SUCCESS","requestId":"req-create","returnObj":{"instanceID":"ecs-test","masterOrderID":"order-test"}}`))
		case TianyiDescribeECSPath:
			if r.Method != http.MethodGet || r.URL.Query().Get("instanceID") != "ecs-test" {
				t.Fatalf("describe request = %s %s", r.Method, r.URL.String())
			}
			_, _ = w.Write([]byte(`{"statusCode":800,"message":"SUCCESS","requestId":"req-describe","returnObj":{"instanceID":"ecs-test","status":"RUNNING","publicIP":"203.0.113.9","privateIP":"10.0.0.9"}}`))
		case TianyiTerminateECSPath:
			_, _ = w.Write([]byte(`{"statusCode":800,"message":"SUCCESS","requestId":"req-delete","returnObj":{}}`))
		default:
			t.Fatalf("unexpected path %s body=%s", r.URL.Path, string(body))
		}
	}))
	defer server.Close()

	userData := base64.StdEncoding.EncodeToString([]byte(vmCloudInitUserData(VMCreateRequest{Image: "image"})))
	client := TianyiVMClient{
		Endpoint:  server.URL,
		AccessKey: "ak-value",
		SecretKey: "secret-value",
		Client:    server.Client(),
		Now:       func() time.Time { return time.Date(2026, 5, 21, 12, 0, 0, 0, time.UTC) },
		RequestID: func() string { return "req-fixed" },
	}
	created, err := client.CreateInstance(context.Background(), TianyiVMCreateRequest{
		ClientToken:      "r_test",
		RegionID:         "huadong1",
		AZName:           "az1",
		DisplayName:      "switchboard-smoke",
		HostName:         "switchboard-smoke",
		FlavorID:         "flavor-test",
		ImageID:          "img-test",
		ImagePublic:      1,
		SystemDiskType:   "SAS",
		SystemDiskSizeGB: 40,
		VPCID:            "vpc-test",
		SubnetID:         "subnet-test",
		SecurityGroupIDs: []string{"sg-test"},
		PublicIP:         true,
		UserDataBase64:   userData,
	})
	if err != nil {
		t.Fatalf("CreateInstance returned error: %v", err)
	}
	if created.InstanceID != "ecs-test" || created.OrderID != "order-test" {
		t.Fatalf("created = %#v", created)
	}
	status, err := client.DescribeInstance(context.Background(), "huadong1", "az1", created.InstanceID)
	if err != nil {
		t.Fatalf("DescribeInstance returned error: %v", err)
	}
	if status.State != "RUNNING" || status.PublicIP != "203.0.113.9" || status.PrivateIP != "10.0.0.9" {
		t.Fatalf("status = %#v", status)
	}
	if err := client.TerminateInstance(context.Background(), "huadong1", "az1", created.InstanceID); err != nil {
		t.Fatalf("TerminateInstance returned error: %v", err)
	}
	if strings.Join(paths, ",") != strings.Join([]string{TianyiCreateECSPath, TianyiDescribeECSPath, TianyiTerminateECSPath}, ",") {
		t.Fatalf("paths = %#v", paths)
	}
}
