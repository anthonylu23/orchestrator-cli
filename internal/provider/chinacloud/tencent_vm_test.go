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

func TestTencentVMClientLifecycleRequests(t *testing.T) {
	var actions []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		action := r.Header.Get("X-TC-Action")
		actions = append(actions, action)
		if r.Method != http.MethodPost {
			t.Fatalf("method = %s", r.Method)
		}
		if r.Header.Get("X-TC-Version") != tencentCVMVersion || r.Header.Get("X-TC-Region") != "ap-guangzhou" {
			t.Fatalf("headers = %#v", r.Header)
		}
		if auth := r.Header.Get("Authorization"); !strings.HasPrefix(auth, "TC3-HMAC-SHA256 Credential=secret-id/") || strings.Contains(auth, "secret-key") {
			t.Fatalf("Authorization = %q", auth)
		}
		switch action {
		case "RunInstances":
			var payload map[string]interface{}
			if err := json.Unmarshal(body, &payload); err != nil {
				t.Fatalf("payload json: %v", err)
			}
			if payload["ImageId"] != "img-test" || payload["UserData"] == "" {
				t.Fatalf("run payload = %#v", payload)
			}
			_, _ = w.Write([]byte(`{"Response":{"InstanceIdSet":["ins-test"],"RequestId":"req-run"}}`))
		case "DescribeInstances":
			_, _ = w.Write([]byte(`{"Response":{"InstanceSet":[{"InstanceId":"ins-test","InstanceState":"RUNNING","PublicIpAddresses":["203.0.113.8"],"PrivateIpAddress":"10.0.0.8"}],"RequestId":"req-describe"}}`))
		case "TerminateInstances":
			_, _ = w.Write([]byte(`{"Response":{"RequestId":"req-terminate"}}`))
		default:
			t.Fatalf("unexpected action %q body=%s", action, string(body))
		}
	}))
	defer server.Close()

	userData := base64.StdEncoding.EncodeToString([]byte(vmCloudInitUserData(VMCreateRequest{Image: "image"})))
	client := TencentVMClient{
		Endpoint:  server.URL + "/",
		Region:    "ap-guangzhou",
		SecretID:  "secret-id",
		SecretKey: "secret-key",
		Client:    server.Client(),
		Now:       func() time.Time { return time.Date(2026, 5, 21, 12, 0, 0, 0, time.UTC) },
	}
	created, err := client.CreateInstance(context.Background(), TencentVMCreateRequest{
		Zone:                    "ap-guangzhou-7",
		ImageID:                 "img-test",
		InstanceType:            "S5.MEDIUM2",
		InstanceName:            "switchboard-smoke",
		KeyPairID:               "skey-test",
		VPCID:                   "vpc-test",
		SubnetID:                "subnet-test",
		SecurityGroupIDs:        []string{"sg-test"},
		SystemDiskType:          "CLOUD_PREMIUM",
		SystemDiskSizeGB:        50,
		InternetMaxBandwidthOut: 10,
		PublicIPAssigned:        true,
		UserDataBase64:          userData,
		ClientToken:             "r_test",
		Tags:                    map[string]string{"switchboard": "true"},
	})
	if err != nil {
		t.Fatalf("CreateInstance returned error: %v", err)
	}
	if created.InstanceID != "ins-test" {
		t.Fatalf("created = %#v", created)
	}
	status, err := client.DescribeInstance(context.Background(), created.InstanceID)
	if err != nil {
		t.Fatalf("DescribeInstance returned error: %v", err)
	}
	if status.State != "RUNNING" || status.PublicIP != "203.0.113.8" || status.PrivateIP != "10.0.0.8" {
		t.Fatalf("status = %#v", status)
	}
	if err := client.TerminateInstance(context.Background(), created.InstanceID); err != nil {
		t.Fatalf("TerminateInstance returned error: %v", err)
	}
	want := []string{"RunInstances", "DescribeInstances", "TerminateInstances"}
	if strings.Join(actions, ",") != strings.Join(want, ",") {
		t.Fatalf("actions = %#v", actions)
	}
}
