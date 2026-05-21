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
)

func TestHuaweiCreateVMBuildsSignedCloudserversRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodPost || r.URL.Path != "/v1/project-test/cloudservers" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		if auth := r.Header.Get("Authorization"); !strings.HasPrefix(auth, "SDK-HMAC-SHA256 Access=ak-value, SignedHeaders=content-type;host;x-sdk-date, Signature=") || strings.Contains(auth, "secret-value") {
			t.Fatalf("Authorization = %q", auth)
		}
		var payload struct {
			Server struct {
				ImageRef  string `json:"imageRef"`
				FlavorRef string `json:"flavorRef"`
				UserData  string `json:"user_data"`
			} `json:"server"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("payload json: %v", err)
		}
		if payload.Server.ImageRef != "huawei-image" || payload.Server.FlavorRef != "s6.large.2" || payload.Server.UserData == "" {
			t.Fatalf("payload = %#v", payload)
		}
		userData, err := base64.StdEncoding.DecodeString(payload.Server.UserData)
		if err != nil {
			t.Fatalf("decode user data: %v", err)
		}
		if !strings.Contains(string(userData), "#cloud-config") || strings.Contains(string(body), "#cloud-config") {
			t.Fatalf("user data encoding/body = %q / %s", string(userData), string(body))
		}
		_, _ = w.Write([]byte(`{"server":{"id":"server-test"}}`))
	}))
	defer server.Close()

	client := newHuaweiClient(HuaweiConfig{
		Region:    "cn-north-4",
		ProjectID: "project-test",
		Endpoint:  server.URL,
		AccessKey: "ak-value",
		SecretKey: "secret-value",
	})
	instance, err := client.CreateVM(context.Background(), VMCreateRequest{
		Name:             "switchboard-smoke",
		Region:           "cn-north-4",
		ImageID:          "huawei-image",
		InstanceType:     "s6.large.2",
		NetworkID:        "vpc-test",
		SubnetID:         "subnet-test",
		SecurityGroupID:  "sg-test",
		SSHKeyName:       "switchboard",
		SystemDiskType:   "SSD",
		SystemDiskSizeGB: 80,
		UserData:         "#cloud-config\nruncmd: []\n",
	})
	if err != nil {
		t.Fatalf("CreateVM returned error: %v", err)
	}
	if instance.ID != "server-test" || instance.State != VMStatePending {
		t.Fatalf("instance = %#v", instance)
	}
}

func TestHuaweiVMStateMapping(t *testing.T) {
	tests := map[string]VMState{
		"BUILD":    VMStatePending,
		"ACTIVE":   VMStateRunning,
		"DELETING": VMStateTerminating,
		"SHUTOFF":  VMStateTerminated,
		"ERROR":    VMStateFailed,
	}
	for status, want := range tests {
		if got := huaweiClientVMState(status); got != want {
			t.Fatalf("%s maps to %s, want %s", status, got, want)
		}
	}
}
