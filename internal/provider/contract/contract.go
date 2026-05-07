package contract

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/anthonylu23/switchboard-cli/internal/app"
	"github.com/anthonylu23/switchboard-cli/internal/routing"
)

type StreamLogsBehavior string

const (
	StreamLogsRequired    StreamLogsBehavior = "required"
	StreamLogsUnsupported StreamLogsBehavior = "unsupported"
	StreamLogsSkip        StreamLogsBehavior = "skip"
)

type Subject struct {
	Name                string
	Adapter             app.ProviderAdapter
	ValidJob            app.JobSpec
	InvalidJob          app.JobSpec
	SubmitRequest       app.SubmitRequest
	ProviderRefPrefix   string
	ExpectedLogText     string
	UnsupportedWorkload app.WorkloadType
	StreamLogs          StreamLogsBehavior
	Cancel              func(t *testing.T, adapter app.ProviderAdapter)
	AssertCapabilities  func(t *testing.T, capabilities app.ProviderCapabilities)
}

type Factory func(t *testing.T) Subject

func Run(t *testing.T, factory Factory) {
	t.Helper()
	t.Run("identity", func(t *testing.T) {
		subject := factory(t)
		if subject.Name == "" {
			t.Fatal("contract subject name is required")
		}
		if subject.Adapter == nil {
			t.Fatal("contract subject adapter is required")
		}
		if got := string(subject.Adapter.Name()); got != subject.Name {
			t.Fatalf("adapter name = %q, want %q", got, subject.Name)
		}
	})

	t.Run("auth", func(t *testing.T) {
		subject := factory(t)
		if err := subject.Adapter.ValidateAuth(context.Background()); err != nil {
			t.Fatalf("ValidateAuth returned error: %v", err)
		}
	})

	t.Run("capabilities", func(t *testing.T) {
		subject := factory(t)
		capabilities, err := subject.Adapter.Capabilities(context.Background())
		if err != nil {
			t.Fatalf("Capabilities returned error: %v", err)
		}
		if !capabilities.SupportsLocalScript && !capabilities.SupportsDockerImage {
			t.Fatalf("provider must support local scripts or docker images: %#v", capabilities)
		}
		if len(capabilities.WorkloadTypes) == 0 {
			t.Fatalf("provider must declare supported workload types: %#v", capabilities)
		}
		if strings.TrimSpace(capabilities.LogMode) == "" {
			t.Fatalf("provider must declare log mode: %#v", capabilities)
		}
		if capabilities.SupportsArtifacts && capabilities.LogMode == "" {
			t.Fatalf("artifact-capable provider must report log mode: %#v", capabilities)
		}
		if capabilities.SupportsObjectStorePull && len(capabilities.SupportedURISchemes) == 0 {
			t.Fatalf("object-store pull support requires URI schemes: %#v", capabilities)
		}
		if subject.AssertCapabilities != nil {
			subject.AssertCapabilities(t, capabilities)
		}
	})

	t.Run("unsupported workload capability", func(t *testing.T) {
		subject := factory(t)
		if subject.UnsupportedWorkload == "" {
			t.Skip("unsupported workload behavior is intentionally skipped for this provider")
		}
		capabilities, err := subject.Adapter.Capabilities(context.Background())
		if err != nil {
			t.Fatalf("Capabilities returned error: %v", err)
		}
		job := subject.ValidJob
		job.Workload.Type = subject.UnsupportedWorkload
		reasons := routing.ValidateCapabilities(job, capabilities)
		if len(reasons) == 0 {
			t.Fatalf("expected unsupported workload %q to be rejected by capabilities %#v", subject.UnsupportedWorkload, capabilities)
		}
	})

	t.Run("validate job", func(t *testing.T) {
		subject := factory(t)
		if report := subject.Adapter.ValidateJob(context.Background(), subject.ValidJob); !report.Supported {
			t.Fatalf("valid job rejected: %#v", report)
		}
		report := subject.Adapter.ValidateJob(context.Background(), subject.InvalidJob)
		if report.Supported {
			t.Fatalf("invalid job supported: %#v", report)
		}
		if len(report.Reasons) == 0 || strings.TrimSpace(report.Reasons[0]) == "" {
			t.Fatalf("invalid job rejection must include a reason: %#v", report)
		}
	})

	t.Run("estimate", func(t *testing.T) {
		subject := factory(t)
		estimate, err := subject.Adapter.Estimate(context.Background(), subject.ValidJob)
		if err != nil {
			t.Fatalf("Estimate returned error: %v", err)
		}
		if estimate.HourlyUSD < 0 {
			t.Fatalf("negative estimate: %#v", estimate)
		}
		if estimate.Currency == "" {
			t.Fatalf("estimate currency is required: %#v", estimate)
		}
	})

	t.Run("submit and status", func(t *testing.T) {
		subject := factory(t)
		var startedRef string
		req := subject.SubmitRequest
		req.OnStarted = func(ref app.ProviderJobRef) error {
			startedRef = ref.ID
			return nil
		}
		result, err := subject.Adapter.Submit(context.Background(), req)
		if err != nil {
			t.Fatalf("Submit returned error: %v", err)
		}
		if result.ExitCode != 0 {
			t.Fatalf("Submit exit code = %d, result = %#v", result.ExitCode, result)
		}
		if result.ProviderJobRef == "" {
			t.Fatalf("provider job ref is required: %#v", result)
		}
		if startedRef == "" {
			t.Fatal("Submit did not call OnStarted")
		}
		if startedRef != result.ProviderJobRef {
			t.Fatalf("started ref %q != result ref %q", startedRef, result.ProviderJobRef)
		}
		if subject.ProviderRefPrefix != "" && !strings.HasPrefix(result.ProviderJobRef, subject.ProviderRefPrefix) {
			t.Fatalf("provider ref %q does not start with %q", result.ProviderJobRef, subject.ProviderRefPrefix)
		}
		if subject.ExpectedLogText != "" {
			content, err := os.ReadFile(filepath.Join(req.RunDir, "logs.txt"))
			if err != nil {
				t.Fatalf("read persisted logs: %v", err)
			}
			if !strings.Contains(string(content), subject.ExpectedLogText) {
				t.Fatalf("logs do not contain %q:\n%s", subject.ExpectedLogText, string(content))
			}
		}
		status, err := subject.Adapter.GetStatus(context.Background(), app.ProviderJobRef{ID: result.ProviderJobRef})
		if err != nil {
			t.Fatalf("GetStatus returned error: %v", err)
		}
		switch status.State {
		case app.AttemptStateRunning, app.AttemptStateSucceeded, app.AttemptStateFailed, app.AttemptStateCanceled:
		default:
			t.Fatalf("unexpected status state: %#v", status)
		}
	})

	t.Run("stream logs", func(t *testing.T) {
		subject := factory(t)
		switch subject.StreamLogs {
		case StreamLogsRequired:
			stream, err := subject.Adapter.StreamLogs(context.Background(), app.LogStreamRequest{Ref: app.ProviderJobRef{ID: "contract-ref"}})
			if err != nil {
				t.Fatalf("StreamLogs returned error: %v", err)
			}
			if stream == nil {
				t.Fatal("StreamLogs returned nil stream")
			}
			if err := stream.Close(); err != nil {
				t.Fatalf("Close returned error: %v", err)
			}
		case StreamLogsUnsupported:
			stream, err := subject.Adapter.StreamLogs(context.Background(), app.LogStreamRequest{Ref: app.ProviderJobRef{ID: "contract-ref"}})
			if err == nil {
				_ = stream.Close()
				t.Fatal("expected StreamLogs unsupported error")
			}
			if stream != nil {
				t.Fatalf("unsupported StreamLogs returned stream: %#v", stream)
			}
		case StreamLogsSkip, "":
			t.Skip("stream logs behavior is intentionally skipped for this provider")
		default:
			t.Fatalf("unknown StreamLogs behavior %q", subject.StreamLogs)
		}
	})

	t.Run("cancel", func(t *testing.T) {
		subject := factory(t)
		if subject.Cancel == nil {
			t.Skip("cancel behavior is intentionally skipped for this provider")
		}
		subject.Cancel(t, subject.Adapter)
	})
}
