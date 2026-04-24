package local

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/anthonylu23/orchestrator-cli/internal/app"
	"github.com/anthonylu23/orchestrator-cli/internal/artifact"
	"github.com/anthonylu23/orchestrator-cli/internal/event"
)

type Provider struct {
	Stdout io.Writer
	Stderr io.Writer
	Now    func() time.Time
}

func New(stdout io.Writer, stderr io.Writer) *Provider {
	return &Provider{Stdout: stdout, Stderr: stderr, Now: time.Now}
}

func (p *Provider) Name() app.ProviderName {
	return app.ProviderLocal
}

func (p *Provider) ValidateAuth(ctx context.Context) error {
	return nil
}

func (p *Provider) Capabilities(ctx context.Context) (app.ProviderCapabilities, error) {
	return app.ProviderCapabilities{
		SupportsOnDemand:    true,
		SupportsLocalScript: true,
		SupportsDataBundle:  true,
	}, nil
}

func (p *Provider) ValidateJob(ctx context.Context, spec app.JobSpec) app.SupportReport {
	if spec.Script == "" {
		return app.SupportReport{Supported: false, Reasons: []string{"script is required"}}
	}
	if _, err := os.Stat(spec.Script); err != nil {
		return app.SupportReport{Supported: false, Reasons: []string{fmt.Sprintf("script %q is not readable", spec.Script)}}
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

	cmdName, args := commandFor(req.JobSpec.Script, req.JobSpec.Args)
	cmd := exec.CommandContext(ctx, cmdName, args...)
	cmd.Dir = req.JobSpec.WorkDir
	cmd.Env = mergedEnv(req.JobSpec.Env, req.RuntimeEnv)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return app.SubmitResult{}, err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return app.SubmitResult{}, err
	}
	if err := cmd.Start(); err != nil {
		return app.SubmitResult{}, fmt.Errorf("start local job: %w", err)
	}

	var wg sync.WaitGroup
	var writeMu sync.Mutex
	wg.Add(2)
	go p.consume(&wg, &writeMu, stdout, p.stdout(), logFile, eventFile, req.RunID, req.AttemptID)
	go p.consume(&wg, &writeMu, stderr, p.stderr(), logFile, eventFile, req.RunID, req.AttemptID)
	waitErr := cmd.Wait()
	wg.Wait()

	if waitErr == nil {
		return app.SubmitResult{ProviderJobRef: "local:" + req.AttemptID, ExitCode: 0, ExitReason: "completed"}, nil
	}
	var exitErr *exec.ExitError
	if errors.As(waitErr, &exitErr) {
		exitCode := exitErr.ExitCode()
		return app.SubmitResult{ProviderJobRef: "local:" + req.AttemptID, ExitCode: exitCode, ExitReason: fmt.Sprintf("process exited with code %d", exitCode)}, nil
	}
	return app.SubmitResult{ProviderJobRef: "local:" + req.AttemptID, ExitCode: 1, ExitReason: waitErr.Error()}, nil
}

func (p *Provider) GetStatus(ctx context.Context, ref app.ProviderJobRef) (app.ProviderJobStatus, error) {
	return app.ProviderJobStatus{State: app.AttemptStateSucceeded}, nil
}

func (p *Provider) StreamLogs(ctx context.Context, req app.LogStreamRequest) (app.LogStream, error) {
	return nil, fmt.Errorf("local provider logs are read from run artifacts")
}

func (p *Provider) Cancel(ctx context.Context, ref app.ProviderJobRef) error {
	return fmt.Errorf("cancel is not implemented for completed local provider references")
}

func (p *Provider) consume(wg *sync.WaitGroup, writeMu *sync.Mutex, reader io.Reader, terminal io.Writer, logFile io.Writer, eventFile io.Writer, runID string, attemptID string) {
	defer wg.Done()
	scanner := bufio.NewScanner(reader)
	for scanner.Scan() {
		line := scanner.Text()
		writeMu.Lock()
		_, _ = fmt.Fprintln(terminal, line)
		_, _ = fmt.Fprintln(logFile, line)
		parsed := event.ParseLine(line, runID, attemptID, p.now())
		if parsed.Structured {
			_ = event.WriteJSONL(eventFile, parsed.Event)
		}
		writeMu.Unlock()
	}
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

func commandFor(script string, scriptArgs []string) (string, []string) {
	if strings.EqualFold(filepath.Ext(script), ".py") {
		args := append([]string{script}, scriptArgs...)
		return pythonCommand(), args
	}
	return script, scriptArgs
}

func pythonCommand() string {
	if runtime.GOOS == "windows" {
		return "python"
	}
	return "python3"
}

func mergedEnv(jobEnv map[string]string, runtimeEnv map[string]string) []string {
	env := os.Environ()
	for k, v := range jobEnv {
		env = append(env, k+"="+v)
	}
	for k, v := range runtimeEnv {
		env = append(env, k+"="+v)
	}
	return env
}
