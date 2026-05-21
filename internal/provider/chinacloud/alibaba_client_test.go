package chinacloud

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestAlibabaCreateVMBuildsSignedRunInstancesRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("method = %s", r.Method)
		}
		query := r.URL.Query()
		if query.Get("Action") != "RunInstances" || query.Get("RegionId") != "cn-hangzhou" || query.Get("ImageId") != "aliyun-image" || query.Get("InstanceType") != "ecs.gn7i-c8g1.2xlarge" {
			t.Fatalf("query = %s", r.URL.RawQuery)
		}
		if query.Get("AccessKeyId") != "ak-value" || query.Get("Signature") == "" || strings.Contains(r.URL.RawQuery, "secret-value") {
			t.Fatalf("signed query leaked or omitted credentials: %s", r.URL.RawQuery)
		}
		userData, err := base64.StdEncoding.DecodeString(query.Get("UserData"))
		if err != nil {
			t.Fatalf("decode user data: %v", err)
		}
		if !strings.Contains(string(userData), "#cloud-config") || strings.Contains(r.URL.RawQuery, "#cloud-config") {
			t.Fatalf("user data encoding/raw query = %q / %s", string(userData), r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"InstanceIdSets":{"InstanceIdSet":["i-test"]}}`))
	}))
	defer server.Close()

	client := newAlibabaClient(AlibabaConfig{
		RegionID:        "cn-hangzhou",
		Endpoint:        server.URL + "/",
		AccessKeyID:     "ak-value",
		AccessKeySecret: "secret-value",
	})
	instance, err := client.CreateVM(context.Background(), VMCreateRequest{
		Name:                  "switchboard-smoke",
		Region:                "cn-hangzhou",
		ImageID:               "aliyun-image",
		InstanceType:          "ecs.gn7i-c8g1.2xlarge",
		SecurityGroupID:       "sg-test",
		VSwitchID:             "vsw-test",
		SSHKeyName:            "switchboard",
		SystemDiskType:        "cloud_essd",
		SystemDiskSizeGB:      80,
		InternetBandwidthMbps: 10,
		UserData:              "#cloud-config\nruncmd: []\n",
	})
	if err != nil {
		t.Fatalf("CreateVM returned error: %v", err)
	}
	if instance.ID != "i-test" || instance.State != VMStatePending {
		t.Fatalf("instance = %#v", instance)
	}
}

func TestAlibabaVMStateMapping(t *testing.T) {
	tests := map[string]VMState{
		"Pending":  VMStatePending,
		"Running":  VMStateRunning,
		"Stopping": VMStateTerminating,
		"Stopped":  VMStateTerminated,
		"Error":    VMStateFailed,
	}
	for status, want := range tests {
		if got := alibabaVMState(status); got != want {
			t.Fatalf("%s maps to %s, want %s", status, got, want)
		}
	}
}
