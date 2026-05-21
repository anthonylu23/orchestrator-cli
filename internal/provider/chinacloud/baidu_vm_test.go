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

func TestBaiduVMClientLifecycleRequests(t *testing.T) {
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requests = append(requests, r.Method+" "+r.URL.Path)
		if auth := r.Header.Get("Authorization"); !strings.HasPrefix(auth, "bce-auth-v1/ak-value/") || strings.Contains(auth, "secret-value") {
			t.Fatalf("Authorization = %q", auth)
		}
		if r.Header.Get("x-bce-date") == "" {
			t.Fatalf("headers = %#v", r.Header)
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v2/instanceBySpec":
			if r.URL.Query().Get("clientToken") != "r_test" {
				t.Fatalf("clientToken = %q", r.URL.Query().Get("clientToken"))
			}
			var payload map[string]interface{}
			if err := json.Unmarshal(body, &payload); err != nil {
				t.Fatalf("payload json: %v", err)
			}
			if payload["imageId"] != "img-test" || payload["userData"] == "" {
				t.Fatalf("create payload = %#v", payload)
			}
			_, _ = w.Write([]byte(`{"instanceIds":["i-baidu"]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/v2/instance/i-baidu":
			_, _ = w.Write([]byte(`{"instance":{"id":"i-baidu","status":"Running","publicIp":"203.0.113.10","internalIp":"10.0.0.10"}}`))
		case r.Method == http.MethodPost && r.URL.Path == "/v2/instance/i-baidu":
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Fatalf("unexpected request %s %s body=%s", r.Method, r.URL.String(), string(body))
		}
	}))
	defer server.Close()

	userData := base64.StdEncoding.EncodeToString([]byte(vmCloudInitUserData(VMCreateRequest{Image: "image"})))
	client := BaiduVMClient{
		Endpoint:        server.URL,
		AccessKeyID:     "ak-value",
		SecretAccessKey: "secret-value",
		Client:          server.Client(),
		Now:             func() time.Time { return time.Date(2026, 5, 21, 12, 0, 0, 0, time.UTC) },
	}
	created, err := client.CreateInstance(context.Background(), BaiduVMCreateRequest{
		ClientToken:         "r_test",
		ZoneName:            "cn-bj-a",
		ImageID:             "img-test",
		InstanceSpec:        "bcc.g1.c1m4",
		Name:                "switchboard-smoke",
		Hostname:            "switchboard-smoke",
		SubnetID:            "subnet-test",
		VPCID:               "vpc-test",
		SecurityGroupID:     "sg-test",
		KeyPairID:           "key-test",
		RootDiskSizeGB:      40,
		RootDiskStorageType: "enhanced_ssd_pl1",
		NetworkCapacityMbps: 10,
		UserDataBase64:      userData,
		Tags:                map[string]string{"switchboard": "true"},
	})
	if err != nil {
		t.Fatalf("CreateInstance returned error: %v", err)
	}
	if created.InstanceID != "i-baidu" {
		t.Fatalf("created = %#v", created)
	}
	status, err := client.DescribeInstance(context.Background(), created.InstanceID)
	if err != nil {
		t.Fatalf("DescribeInstance returned error: %v", err)
	}
	if status.State != "Running" || status.PublicIP != "203.0.113.10" || status.PrivateIP != "10.0.0.10" {
		t.Fatalf("status = %#v", status)
	}
	if err := client.TerminateInstance(context.Background(), created.InstanceID); err != nil {
		t.Fatalf("TerminateInstance returned error: %v", err)
	}
	if strings.Join(requests, ",") != strings.Join([]string{"POST /v2/instanceBySpec", "GET /v2/instance/i-baidu", "POST /v2/instance/i-baidu"}, ",") {
		t.Fatalf("requests = %#v", requests)
	}
}
