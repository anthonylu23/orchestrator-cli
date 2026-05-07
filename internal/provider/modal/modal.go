package modal

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/anthonylu23/switchboard-cli/internal/app"
	"github.com/anthonylu23/switchboard-cli/internal/artifact"
	"github.com/anthonylu23/switchboard-cli/internal/event"
	"github.com/anthonylu23/switchboard-cli/internal/redact"
)

const ProviderName = app.ProviderName("modal-sandbox")

//go:embed runner.py
var runnerFS embed.FS

type Provider struct {
	Stdout io.Writer
	Stderr io.Writer
	Now    func() time.Time
}

func New(stdout io.Writer, stderr io.Writer) *Provider {
	return &Provider{Stdout: stdout, Stderr: stderr, Now: time.Now}
}

func (p *Provider) Name() app.ProviderName {
	return ProviderName
}

func (p *Provider) ValidateAuth(ctx context.Context) error {
	result, err := p.runHelper(ctx, "doctor")
	if err != nil {
		message := result.Message
		if message == "" {
			message = err.Error()
		}
		return &app.ProviderError{Kind: app.ProviderErrorAuth, Message: message, Err: err}
	}
	if result.Kind != "" && result.Event == "error" {
		return &app.ProviderError{Kind: providerErrorKind(result.Kind), Message: result.Message}
	}
	return nil
}

func (p *Provider) Capabilities(ctx context.Context) (app.ProviderCapabilities, error) {
	return app.ProviderCapabilities{
		WorkloadTypes:        []string{string(app.WorkloadTypeEvaluation), string(app.WorkloadTypeBatchInference)},
		LogMode:              "artifact",
		SupportsOnDemand:     true,
		SupportsLocalScript:  true,
		SupportsDataBundle:   true,
		SupportsArtifacts:    true,
		SupportsCancel:       true,
		SupportsCostEstimate: false,
		Remote:               true,
	}, nil
}

func (p *Provider) ValidateJob(ctx context.Context, spec app.JobSpec) app.SupportReport {
	var reasons []string
	if spec.Script == "" {
		reasons = append(reasons, "script is required")
	} else if _, err := os.Stat(spec.Script); err != nil {
		reasons = append(reasons, fmt.Sprintf("script %q is not readable", spec.Script))
	}
	if spec.Workload.Dataset.URI != "" {
		reasons = append(reasons, "modal-sandbox provider does not fetch dataset URIs yet")
	}
	if spec.Workload.Dataset.Path != "" {
		if _, err := os.Stat(spec.Workload.Dataset.Path); err != nil {
			reasons = append(reasons, fmt.Sprintf("dataset path %q is not readable", spec.Workload.Dataset.Path))
		}
	}
	for _, input := range spec.Data {
		if input.Mode == app.DataInputModeURI {
			reasons = append(reasons, fmt.Sprintf("modal-sandbox provider does not fetch URI data input %q yet", input.Name))
		}
	}
	if len(reasons) > 0 {
		return app.SupportReport{Supported: false, Reasons: reasons}
	}
	return app.SupportReport{Supported: true}
}

func (p *Provider) Estimate(ctx context.Context, spec app.JobSpec) (app.CostEstimate, error) {
	return app.CostEstimate{HourlyUSD: 0, Currency: "USD"}, nil
}

func (p *Provider) Submit(ctx context.Context, req app.SubmitRequest) (app.SubmitResult, error) {
	paths := artifact.ForRun(filepath.Dir(filepath.Dir(req.RunDir)), req.RunID)
	logFile, err := os.OpenFile(paths.Logs, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return app.SubmitResult{}, fmt.Errorf("open logs artifact: %w", err)
	}
	defer logFile.Close()
	eventFile, err := os.OpenFile(paths.EventsJSONL, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return app.SubmitResult{}, fmt.Errorf("open events artifact: %w", err)
	}
	defer eventFile.Close()

	tempDir, err := os.MkdirTemp("", "cloudtune-modal-*")
	if err != nil {
		return app.SubmitResult{}, err
	}
	defer os.RemoveAll(tempDir)
	archivePath := filepath.Join(tempDir, "artifacts.tar.gz")
	requestPath := filepath.Join(tempDir, "request.json")
	payload := modalRunRequest{
		AppName:        "cloudtune-orchestrator",
		RunID:          req.RunID,
		AttemptID:      req.AttemptID,
		Script:         req.JobSpec.Script,
		Args:           append([]string(nil), req.JobSpec.Args...),
		Env:            cloneMap(req.JobSpec.Env),
		RuntimeEnv:     cloneMap(req.RuntimeEnv),
		DatasetPath:    req.JobSpec.Workload.Dataset.Path,
		LocalArchive:   archivePath,
		TimeoutSeconds: 300,
	}
	if err := writeJSON(requestPath, payload); err != nil {
		return app.SubmitResult{}, err
	}

	redactor := redact.FromEnvironment(req.JobSpec.Env, req.RuntimeEnv)
	result, err := p.runHelper(ctx, "run", requestPath)
	if result.ProviderRef != "" && req.OnStarted != nil {
		if startErr := req.OnStarted(app.ProviderJobRef{ID: result.ProviderRef}); startErr != nil {
			return app.SubmitResult{}, startErr
		}
	}
	writeRemoteOutput(logFile, eventFile, p.stdout(), result.Stdout, req.RunID, req.AttemptID, redactor, p.now())
	writeRemoteOutput(logFile, eventFile, p.stderr(), result.Stderr, req.RunID, req.AttemptID, redactor, p.now())
	if result.ArchiveFetched {
		if err := unpackArchive(archivePath, paths.RunDir); err != nil {
			return app.SubmitResult{ProviderJobRef: result.ProviderRef, ExitCode: 1, ExitReason: err.Error()}, err
		}
	} else if result.ArchiveStderr != "" {
		_, _ = fmt.Fprintf(logFile, "modal artifact fetch warning: %s\n", redactor.String(result.ArchiveStderr))
	}
	if err != nil {
		kind := providerErrorKind(result.Kind)
		message := result.Message
		if message == "" {
			message = err.Error()
		}
		return app.SubmitResult{ProviderJobRef: result.ProviderRef, ExitCode: result.exitCode(), ExitReason: redactor.String(message)}, &app.ProviderError{Kind: kind, Message: redactor.String(message), Err: err}
	}
	if result.Event == "error" {
		kind := providerErrorKind(result.Kind)
		message := result.Message
		return app.SubmitResult{ProviderJobRef: result.ProviderRef, ExitCode: result.exitCode(), ExitReason: redactor.String(message)}, &app.ProviderError{Kind: kind, Message: redactor.String(message)}
	}
	reason := "completed"
	if result.ExitCode != 0 {
		reason = fmt.Sprintf("modal sandbox process exited with code %d", result.ExitCode)
	}
	return app.SubmitResult{ProviderJobRef: result.ProviderRef, ExitCode: result.ExitCode, ExitReason: reason}, nil
}

func (p *Provider) GetStatus(ctx context.Context, ref app.ProviderJobRef) (app.ProviderJobStatus, error) {
	sandboxID := strings.TrimPrefix(ref.ID, "modal-sandbox:")
	if sandboxID == "" || sandboxID == ref.ID {
		return app.ProviderJobStatus{}, fmt.Errorf("invalid modal-sandbox provider ref %q", ref.ID)
	}
	result, err := p.runHelper(ctx, "status", sandboxID)
	if err != nil {
		return app.ProviderJobStatus{}, err
	}
	if result.Event == "error" {
		return app.ProviderJobStatus{}, &app.ProviderError{Kind: providerErrorKind(result.Kind), Message: result.Message}
	}
	switch result.State {
	case string(app.AttemptStateRunning):
		return app.ProviderJobStatus{State: app.AttemptStateRunning}, nil
	case string(app.AttemptStateSucceeded):
		return app.ProviderJobStatus{State: app.AttemptStateSucceeded}, nil
	case string(app.AttemptStateFailed):
		return app.ProviderJobStatus{State: app.AttemptStateFailed}, nil
	default:
		return app.ProviderJobStatus{}, fmt.Errorf("unexpected modal-sandbox status %q", result.State)
	}
}

func (p *Provider) StreamLogs(ctx context.Context, req app.LogStreamRequest) (app.LogStream, error) {
	return nil, fmt.Errorf("modal-sandbox provider logs are read from run artifacts")
}

func (p *Provider) Cancel(ctx context.Context, ref app.ProviderJobRef) error {
	sandboxID := strings.TrimPrefix(ref.ID, "modal-sandbox:")
	if sandboxID == "" || sandboxID == ref.ID {
		return fmt.Errorf("invalid modal-sandbox provider ref %q", ref.ID)
	}
	result, err := p.runHelper(ctx, "cancel", sandboxID)
	if err != nil {
		return err
	}
	if result.Event == "error" {
		return &app.ProviderError{Kind: providerErrorKind(result.Kind), Message: result.Message}
	}
	return nil
}

func (p *Provider) runHelper(ctx context.Context, args ...string) (modalRunnerEvent, error) {
	python, err := pythonCommand()
	if err != nil {
		return modalRunnerEvent{Event: "error", Kind: string(app.ProviderErrorAuth), Message: "python3 not found on PATH", ExitCode: 1}, err
	}
	tempDir, err := os.MkdirTemp("", "cloudtune-modal-runner-*")
	if err != nil {
		return modalRunnerEvent{}, err
	}
	defer os.RemoveAll(tempDir)
	runnerPath := filepath.Join(tempDir, "runner.py")
	content, err := runnerFS.ReadFile("runner.py")
	if err != nil {
		return modalRunnerEvent{}, err
	}
	if err := os.WriteFile(runnerPath, content, 0o700); err != nil {
		return modalRunnerEvent{}, err
	}
	commandArgs := append([]string{runnerPath}, args...)
	cmd := exec.CommandContext(ctx, python, commandArgs...)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return modalRunnerEvent{}, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return modalRunnerEvent{}, err
	}
	if err := cmd.Start(); err != nil {
		return modalRunnerEvent{}, err
	}
	var last modalRunnerEvent
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 64*1024), 10*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "__CLOUDTUNE_MODAL__ ") {
			continue
		}
		var item modalRunnerEvent
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "__CLOUDTUNE_MODAL__ ")), &item); err != nil {
			continue
		}
		if item.Event == "provider_ref" {
			last.ProviderRef = item.ProviderRef
			continue
		}
		if last.ProviderRef != "" && item.ProviderRef == "" {
			item.ProviderRef = last.ProviderRef
		}
		last = item
	}
	stderrContent, _ := io.ReadAll(stderr)
	waitErr := cmd.Wait()
	if scannerErr := scanner.Err(); scannerErr != nil {
		return last, scannerErr
	}
	if waitErr != nil {
		if last.Message == "" {
			last.Message = strings.TrimSpace(string(stderrContent))
		}
		if last.Message == "" {
			last.Message = waitErr.Error()
		}
		if last.ExitCode == 0 {
			last.ExitCode = 1
		}
		return last, waitErr
	}
	return last, nil
}

func writeRemoteOutput(logFile io.Writer, eventFile io.Writer, terminal io.Writer, content string, runID string, attemptID string, redactor redact.Redactor, now time.Time) {
	scanner := bufio.NewScanner(strings.NewReader(content))
	for scanner.Scan() {
		line := scanner.Text()
		redacted := redactor.Line(line)
		_, _ = fmt.Fprintln(logFile, redacted)
		_, _ = fmt.Fprintln(terminal, redacted)
		parsed := event.ParseLine(line, runID, attemptID, now)
		if parsed.Structured {
			_ = event.WriteJSONL(eventFile, redactor.Event(parsed.Event))
		}
	}
}

func unpackArchive(archivePath string, destination string) error {
	file, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer file.Close()
	gzipReader, err := gzip.NewReader(file)
	if err != nil {
		return err
	}
	defer gzipReader.Close()
	tarReader := tar.NewReader(gzipReader)
	for {
		header, err := tarReader.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		target, err := safeJoin(destination, header.Name)
		if err != nil {
			return err
		}
		switch header.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			out, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(header.Mode))
			if err != nil {
				return err
			}
			if _, err := io.Copy(out, tarReader); err != nil {
				_ = out.Close()
				return err
			}
			if err := out.Close(); err != nil {
				return err
			}
		}
	}
}

func safeJoin(root string, name string) (string, error) {
	clean := filepath.Clean(name)
	if clean == "." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) || filepath.IsAbs(clean) {
		return "", fmt.Errorf("unsafe archive path %q", name)
	}
	target := filepath.Join(root, clean)
	rel, err := filepath.Rel(root, target)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("unsafe archive path %q", name)
	}
	return target, nil
}

func providerErrorKind(value string) app.ProviderErrorKind {
	switch app.ProviderErrorKind(value) {
	case app.ProviderErrorAuth, app.ProviderErrorCapacity, app.ProviderErrorQuota, app.ProviderErrorInvalidSpec, app.ProviderErrorNetwork, app.ProviderErrorInternal, app.ProviderErrorRuntime:
		return app.ProviderErrorKind(value)
	default:
		return app.ProviderErrorInternal
	}
}

func pythonCommand() (string, error) {
	if runtime.GOOS == "windows" {
		if path, err := exec.LookPath("python"); err == nil {
			return path, nil
		}
	}
	if path, err := exec.LookPath("python3"); err == nil {
		return path, nil
	}
	return exec.LookPath("python")
}

func writeJSON(path string, value interface{}) error {
	content, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return os.WriteFile(path, content, 0o600)
}

func cloneMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func (p *Provider) stdout() io.Writer {
	if p.Stdout != nil {
		return p.Stdout
	}
	return os.Stdout
}

func (p *Provider) stderr() io.Writer {
	if p.Stderr != nil {
		return p.Stderr
	}
	return os.Stderr
}

func (p *Provider) now() time.Time {
	if p.Now != nil {
		return p.Now().UTC()
	}
	return time.Now().UTC()
}

type modalRunRequest struct {
	AppName        string            `json:"app_name"`
	RunID          string            `json:"run_id"`
	AttemptID      string            `json:"attempt_id"`
	Script         string            `json:"script"`
	Args           []string          `json:"args"`
	Env            map[string]string `json:"env"`
	RuntimeEnv     map[string]string `json:"runtime_env"`
	DatasetPath    string            `json:"dataset_path"`
	LocalArchive   string            `json:"local_archive"`
	TimeoutSeconds int               `json:"timeout_seconds"`
}

type modalRunnerEvent struct {
	Event          string `json:"event"`
	Kind           string `json:"kind,omitempty"`
	Message        string `json:"message,omitempty"`
	Traceback      string `json:"traceback,omitempty"`
	ProviderRef    string `json:"provider_ref,omitempty"`
	SandboxID      string `json:"sandbox_id,omitempty"`
	ExitCode       int    `json:"exit_code,omitempty"`
	Stdout         string `json:"stdout,omitempty"`
	Stderr         string `json:"stderr,omitempty"`
	ArchiveFetched bool   `json:"archive_fetched,omitempty"`
	ArchiveStderr  string `json:"archive_stderr,omitempty"`
	State          string `json:"state,omitempty"`
}

func (e modalRunnerEvent) exitCode() int {
	if e.ExitCode != 0 {
		return e.ExitCode
	}
	return 1
}
