package chinacloud

import (
	"context"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anthonylu23/switchboard-cli/internal/app"
	"github.com/anthonylu23/switchboard-cli/internal/artifact"
)

func TestVMCloudInitUserData(t *testing.T) {
	userData := vmCloudInitUserData(VMCreateRequest{
		Image:   "registry.example.com/switchboard/train:latest",
		Command: []string{"python", "/app/train.py"},
		Args:    []string{"--epochs", "1"},
		Env: map[string]string{
			"SWITCHBOARD_CHECKPOINT_DIR": "/tmp/switchboard/checkpoints",
			"SWITCHBOARD_EVENTS_PATH":    "/tmp/switchboard/events.jsonl",
			"TRAIN_MODE":                 "smoke",
		},
		WorkDir: "/app",
	})
	for _, want := range []string{
		"#cloud-config",
		"switchboard china vm job starting",
		"docker pull 'registry.example.com/switchboard/train:latest'",
		"printf '%s\\n' 'SWITCHBOARD_EVENTS_PATH=/tmp/switchboard/events.jsonl' >> '/tmp/switchboard/container.env'",
		"printf '%s\\n' 'SWITCHBOARD_CHECKPOINT_DIR=/tmp/switchboard/checkpoints' >> '/tmp/switchboard/container.env'",
		"'--env-file' '/tmp/switchboard/container.env'",
		"'-w' '/app'",
		"'python' '/app/train.py' '--epochs' '1'",
		"runcmd:",
	} {
		if !strings.Contains(userData, want) {
			t.Fatalf("expected %q in user data:\n%s", want, userData)
		}
	}
}

func TestVMRuntimeSubmitCollectsArtifactsAndTerminates(t *testing.T) {
	home := t.TempDir()
	paths := artifact.ForRun(home, "r_china")
	if err := artifact.EnsureRun(paths); err != nil {
		t.Fatalf("ensure run: %v", err)
	}
	client := &fakeChinaVMClient{
		createInstance: VMInstance{ID: "i-123", State: VMStatePending, NativeState: "Pending", Region: "cn-hangzhou"},
		getInstances: []VMInstance{
			{ID: "i-123", State: VMStateRunning, NativeState: "Running", PublicIP: "203.0.113.10", PrivateIP: "10.0.0.10", Region: "cn-hangzhou"},
		},
	}
	remote := &fakeChinaRemote{files: map[string]string{
		vmRemoteLogsPath:   "plain log\n{\"type\":\"metric\",\"step\":1,\"metrics\":{\"loss\":0.5}}\n",
		vmRemoteEventsPath: "{\"type\":\"checkpoint\",\"step\":1,\"checkpoint_uri\":\"oss://bucket/ckpt.pt\"}\n",
		vmRemoteExitPath:   "{\"exit_code\":0,\"exit_reason\":\"completed\"}",
	}}
	provider := NewAlibabaWithClient(testChinaAlibabaConfig(), client, remote, nil, nil)
	provider.Sleep = func(ctx context.Context, d time.Duration) error { return nil }
	var started string
	var resources []app.ProviderResource
	result, err := provider.Submit(context.Background(), app.SubmitRequest{
		JobSpec: app.JobSpec{
			Name:  "valid",
			Image: "registry.example.com/smoke:latest",
			Env:   map[string]string{"SWITCHBOARD_RUN_ID": "job-value"},
		},
		RunID:      "r_china",
		AttemptID:  "a_china",
		RuntimeEnv: map[string]string{"SWITCHBOARD_RUN_ID": "runtime-value"},
		RunDir:     paths.RunDir,
		OnStarted: func(ref app.ProviderJobRef) error {
			started = ref.ID
			return nil
		},
		OnResourceCreated: func(resource app.ProviderResource) error {
			resources = append(resources, resource)
			return nil
		},
		OnResourceUpdated: func(resource app.ProviderResource) error {
			resources = append(resources, resource)
			return nil
		},
	})
	if err != nil {
		t.Fatalf("Submit returned error: %v", err)
	}
	if result.ProviderJobRef != "alibaba-cloud:i-123" || started != result.ProviderJobRef || result.ExitCode != 0 {
		t.Fatalf("result=%#v started=%q", result, started)
	}
	if client.createReq.Image != "registry.example.com/smoke:latest" || client.createReq.Env["SWITCHBOARD_RUN_ID"] != "runtime-value" {
		t.Fatalf("create request = %#v", client.createReq)
	}
	if len(client.terminated) != 1 || client.terminated[0] != "i-123" {
		t.Fatalf("terminated = %#v", client.terminated)
	}
	if len(resources) != 3 || resources[0].State != app.ProviderResourceStateBooting || resources[1].State != app.ProviderResourceStateRunning || resources[2].State != app.ProviderResourceStateTerminating {
		t.Fatalf("resources = %#v", resources)
	}
	logs, err := os.ReadFile(paths.Logs)
	if err != nil {
		t.Fatalf("read logs: %v", err)
	}
	if !strings.Contains(string(logs), "plain log") {
		t.Fatalf("logs = %s", string(logs))
	}
	events, err := os.ReadFile(paths.EventsJSONL)
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	if !strings.Contains(string(events), `"type":"metric"`) || !strings.Contains(string(events), `"type":"checkpoint"`) {
		t.Fatalf("events = %s", string(events))
	}
}

func TestVMRuntimeValidateJobRequiresImage(t *testing.T) {
	provider := NewAlibabaWithClient(testChinaAlibabaConfig(), &fakeChinaVMClient{}, &fakeChinaRemote{}, nil, nil)
	report := provider.ValidateJob(context.Background(), app.JobSpec{Name: "bad", Script: "train.py"})
	if report.Supported {
		t.Fatalf("expected unsupported report: %#v", report)
	}
	for _, want := range []string{"requires job.image", "does not package local scripts"} {
		if !containsChinaReason(report.Reasons, want) {
			t.Fatalf("expected %q in reasons %#v", want, report.Reasons)
		}
	}
	report = provider.ValidateJob(context.Background(), app.JobSpec{Name: "valid", Image: "image"})
	if !report.Supported {
		t.Fatalf("expected supported report: %#v", report)
	}
}

func TestVMRuntimeCancelTerminatesInstance(t *testing.T) {
	client := &fakeChinaVMClient{}
	provider := NewAlibabaWithClient(testChinaAlibabaConfig(), client, &fakeChinaRemote{}, nil, nil)
	if err := provider.Cancel(context.Background(), app.ProviderJobRef{ID: "alibaba-cloud:i-cancel"}); err != nil {
		t.Fatalf("Cancel returned error: %v", err)
	}
	if len(client.terminated) != 1 || client.terminated[0] != "i-cancel" {
		t.Fatalf("terminated = %#v", client.terminated)
	}
}

func testChinaAlibabaConfig() AlibabaConfig {
	return AlibabaConfig{
		RegionID:               "cn-hangzhou",
		InstanceType:           "ecs.gn7i-c8g1.2xlarge",
		ImageID:                "aliyun_3_x64_20G_alibase_20240528.vhd",
		KeyPairName:            "switchboard",
		SSHPrivateKey:          "testdata/id_ed25519",
		PollIntervalSeconds:    1,
		SSHReadyTimeoutSeconds: 1,
		SSHConnectTimeoutSecs:  1,
	}
}

type fakeChinaVMClient struct {
	validateErr    error
	createErr      error
	createReq      VMCreateRequest
	createInstance VMInstance
	getInstances   []VMInstance
	getErr         error
	terminated     []string
	mu             sync.Mutex
}

func (c *fakeChinaVMClient) ValidateAuth(ctx context.Context) error {
	return c.validateErr
}

func (c *fakeChinaVMClient) CreateVM(ctx context.Context, req VMCreateRequest) (VMInstance, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.createReq = req
	if c.createErr != nil {
		return VMInstance{}, c.createErr
	}
	if c.createInstance.ID == "" {
		return VMInstance{ID: "i-fake", State: VMStatePending, Region: req.Region, Zone: req.Zone}, nil
	}
	return c.createInstance, nil
}

func (c *fakeChinaVMClient) GetVM(ctx context.Context, id string) (VMInstance, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.getErr != nil {
		return VMInstance{}, c.getErr
	}
	if len(c.getInstances) == 0 {
		return VMInstance{ID: id, State: VMStateRunning, PublicIP: "203.0.113.5"}, nil
	}
	instance := c.getInstances[0]
	if len(c.getInstances) > 1 {
		c.getInstances = c.getInstances[1:]
	}
	return instance, nil
}

func (c *fakeChinaVMClient) TerminateVM(ctx context.Context, id string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.terminated = append(c.terminated, id)
	return nil
}

type fakeChinaRemote struct {
	waitErr error
	files   map[string]string
}

func (r *fakeChinaRemote) WaitForReady(ctx context.Context, target VMRemoteTarget, timeout time.Duration, sleep func(context.Context, time.Duration) error) error {
	return r.waitErr
}

func (r *fakeChinaRemote) ReadFile(ctx context.Context, target VMRemoteTarget, path string) (string, error) {
	content, ok := r.files[path]
	if !ok {
		return "", os.ErrNotExist
	}
	return content, nil
}

func containsChinaReason(reasons []string, want string) bool {
	for _, reason := range reasons {
		if strings.Contains(reason, want) {
			return true
		}
	}
	return false
}
