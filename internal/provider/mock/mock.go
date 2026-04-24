package mock

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/anthonylu23/orchestrator-cli/internal/app"
	"github.com/anthonylu23/orchestrator-cli/internal/artifact"
	"github.com/anthonylu23/orchestrator-cli/internal/event"
)

const (
	FailureNone     = ""
	FailureCapacity = "capacity"
	FailureNetwork  = "network"
	FailureInternal = "internal"
	FailureRuntime  = "runtime"
)

type Config struct {
	Name        string
	HourlyCost  float64
	FailureMode string
	Events      []app.Event
}

type Provider struct {
	config Config
	Stdout io.Writer
	Stderr io.Writer
	Now    func() time.Time
}

func New(config Config, stdout io.Writer, stderr io.Writer) *Provider {
	return &Provider{config: config, Stdout: stdout, Stderr: stderr, Now: time.Now}
}

func (p *Provider) Name() app.ProviderName {
	return app.ProviderName(p.config.Name)
}

func (p *Provider) ValidateAuth(ctx context.Context) error {
	return nil
}

func (p *Provider) Capabilities(ctx context.Context) (app.ProviderCapabilities, error) {
	return app.ProviderCapabilities{
		SupportsOnDemand:        true,
		SupportsLocalScript:     true,
		SupportsDataBundle:      true,
		SupportedURISchemes:     []string{"http", "https", "s3", "gs"},
		SupportsObjectStorePull: true,
	}, nil
}

func (p *Provider) ValidateJob(ctx context.Context, spec app.JobSpec) app.SupportReport {
	return app.SupportReport{Supported: true}
}

func (p *Provider) Estimate(ctx context.Context, spec app.JobSpec) (app.CostEstimate, error) {
	return app.CostEstimate{HourlyUSD: p.config.HourlyCost, Currency: "USD"}, nil
}

func (p *Provider) Submit(ctx context.Context, req app.SubmitRequest) (app.SubmitResult, error) {
	providerRef := fmt.Sprintf("mock:%s:%s", p.config.Name, req.AttemptID)
	if req.OnStarted != nil {
		if err := req.OnStarted(app.ProviderJobRef{ID: providerRef}); err != nil {
			return app.SubmitResult{}, err
		}
	}
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

	p.writeLog(logFile, fmt.Sprintf("mock provider %s started", p.config.Name))
	if req.ResumeFrom != nil {
		p.writeLog(logFile, fmt.Sprintf("resuming from checkpoint step %d: %s", req.ResumeFrom.Step, req.ResumeFrom.URI))
	}
	for _, scripted := range p.config.Events {
		ev := scripted
		ev.RunID = req.RunID
		ev.AttemptID = req.AttemptID
		if ev.Timestamp.IsZero() {
			ev.Timestamp = p.now()
		}
		if ev.Type == app.EventTypeLog {
			p.writeLog(logFile, ev.Message)
			continue
		}
		if err := event.WriteJSONL(eventFile, ev); err != nil {
			return app.SubmitResult{}, err
		}
		encoded := fmt.Sprintf("%s", ev.Type)
		if ev.Step != nil {
			encoded = fmt.Sprintf("%s step=%d", encoded, *ev.Step)
		}
		p.writeLog(logFile, encoded)
	}
	if err := p.failureError(); err != nil {
		p.writeLog(logFile, err.Error())
		return app.SubmitResult{ProviderJobRef: providerRef, ExitCode: 1, ExitReason: err.Error()}, err
	}
	p.writeLog(logFile, fmt.Sprintf("mock provider %s completed", p.config.Name))
	return app.SubmitResult{ProviderJobRef: providerRef, ExitCode: 0, ExitReason: "completed"}, nil
}

func (p *Provider) GetStatus(ctx context.Context, ref app.ProviderJobRef) (app.ProviderJobStatus, error) {
	return app.ProviderJobStatus{State: app.AttemptStateSucceeded}, nil
}

func (p *Provider) StreamLogs(ctx context.Context, req app.LogStreamRequest) (app.LogStream, error) {
	return nil, fmt.Errorf("mock provider logs are read from run artifacts")
}

func (p *Provider) Cancel(ctx context.Context, ref app.ProviderJobRef) error {
	return nil
}

func (p *Provider) writeLog(logFile io.Writer, line string) {
	if line == "" {
		return
	}
	_, _ = fmt.Fprintln(logFile, line)
	_, _ = fmt.Fprintln(p.stdout(), line)
}

func (p *Provider) failureError() *app.ProviderError {
	switch p.config.FailureMode {
	case FailureNone:
		return nil
	case FailureCapacity:
		return &app.ProviderError{Kind: app.ProviderErrorCapacity, Message: fmt.Sprintf("%s capacity interruption", p.config.Name)}
	case FailureNetwork:
		return &app.ProviderError{Kind: app.ProviderErrorNetwork, Message: fmt.Sprintf("%s network failure", p.config.Name)}
	case FailureInternal:
		return &app.ProviderError{Kind: app.ProviderErrorInternal, Message: fmt.Sprintf("%s internal failure", p.config.Name)}
	case FailureRuntime:
		return &app.ProviderError{Kind: app.ProviderErrorRuntime, Message: fmt.Sprintf("%s runtime failure", p.config.Name)}
	default:
		return nil
	}
}

func (p *Provider) stdout() io.Writer {
	if p.Stdout != nil {
		return p.Stdout
	}
	return os.Stdout
}

func (p *Provider) now() time.Time {
	if p.Now != nil {
		return p.Now().UTC()
	}
	return time.Now().UTC()
}
