package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/anthonylu23/switchboard-cli/internal/app"
	"github.com/anthonylu23/switchboard-cli/internal/config"
	"github.com/anthonylu23/switchboard-cli/internal/routing"
	"github.com/spf13/cobra"
)

type doctorStatus string

const (
	doctorOK   doctorStatus = "ok"
	doctorWarn doctorStatus = "warn"
	doctorFail doctorStatus = "fail"
)

type doctorCheck struct {
	Section string       `json:"section"`
	Name    string       `json:"name"`
	Status  doctorStatus `json:"status"`
	Message string       `json:"message,omitempty"`
}

type doctorReport struct {
	Home     string        `json:"home"`
	Provider string        `json:"provider,omitempty"`
	Ready    bool          `json:"ready"`
	Checks   []doctorCheck `json:"checks"`
}

func newDoctorCommand(opts Options, home *string) *cobra.Command {
	var providerName string
	var configPath string
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Check CloudTune runtime and provider readiness",
		RunE: func(cmd *cobra.Command, args []string) error {
			resolvedHome, err := resolveHome(*home)
			if err != nil {
				return err
			}
			report := runDoctor(cmd.Context(), doctorOptions{
				Home:       resolvedHome,
				Provider:   providerName,
				ConfigPath: configPath,
				CLIOptions: opts,
			})
			if asJSON {
				if err := json.NewEncoder(opts.Stdout).Encode(report); err != nil {
					return err
				}
			} else {
				printDoctorReport(opts.Stdout, report)
			}
			if !report.Ready {
				return exitCodeError{code: exitCodeInternal}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&providerName, "provider", "", "Provider to diagnose")
	cmd.Flags().StringVar(&configPath, "config", "", "Optional workload config to validate")
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print JSON")
	return cmd
}

type doctorOptions struct {
	Home       string
	Provider   string
	ConfigPath string
	CLIOptions Options
}

func runDoctor(ctx context.Context, opts doctorOptions) doctorReport {
	report := doctorReport{Home: opts.Home, Provider: opts.Provider, Ready: true}
	add := func(section string, name string, status doctorStatus, message string) {
		report.Checks = append(report.Checks, doctorCheck{Section: section, Name: name, Status: status, Message: message})
		if status == doctorFail {
			report.Ready = false
		}
	}

	addPathReadiness(add, "Core", "home", opts.Home)
	addPathReadiness(add, "Core", "runs_dir", filepath.Join(opts.Home, "runs"))
	addCommandCheck(ctx, add, "Core", "git", "git", "rev-parse", "--is-inside-work-tree")
	addPythonCheck(ctx, add, "Core", "python")

	var resolved *config.ResolvedTrainConfig
	if opts.ConfigPath != "" {
		flags := config.TrainFlags{ConfigPath: opts.ConfigPath, SwitchboardHome: opts.Home}
		if opts.Provider != "" && !isModalAlias(opts.Provider) {
			flags.Provider = opts.Provider
		}
		cfg, err := config.LoadTrain(flags)
		if err != nil {
			add("Config", "load", doctorFail, err.Error())
		} else {
			resolved = &cfg
			add("Config", "load", doctorOK, cfg.ConfigPath)
			add("Config", "config_hash", doctorOK, cfg.ConfigHash)
			addFileCheck(add, "Config", "script", cfg.Job.Script)
			if cfg.Job.Workload.Dataset.Path != "" {
				addFileCheck(add, "Config", "dataset", cfg.Job.Workload.Dataset.Path)
			}
			if cfg.Job.Outputs.SaveTo != "" {
				addOutputPathCheck(add, cfg.Job.Outputs.SaveTo)
			}
		}
	}

	if opts.Provider != "" {
		addProviderChecks(ctx, add, opts, resolved)
	}

	sort.SliceStable(report.Checks, func(i, j int) bool {
		if report.Checks[i].Section == report.Checks[j].Section {
			return report.Checks[i].Name < report.Checks[j].Name
		}
		return report.Checks[i].Section < report.Checks[j].Section
	})
	return report
}

func addProviderChecks(ctx context.Context, add func(string, string, doctorStatus, string), opts doctorOptions, resolved *config.ResolvedTrainConfig) {
	registry := buildProviderRegistry(opts.CLIOptions, config.MockConfig{})
	adapter, err := registry.Get(opts.Provider)
	if err != nil {
		if isModalAlias(opts.Provider) {
			add("Provider", "registered", doctorFail, "modal-sandbox provider is not implemented in this branch")
			addModalDiagnostics(ctx, add)
			return
		}
		add("Provider", "registered", doctorFail, err.Error())
		return
	}
	add("Provider", "registered", doctorOK, string(adapter.Name()))
	if isModalAlias(opts.Provider) {
		addModalDiagnostics(ctx, add)
	}
	if err := adapter.ValidateAuth(ctx); err != nil {
		add("Provider", "auth", doctorFail, err.Error())
	} else {
		add("Provider", "auth", doctorOK, "validated")
	}
	capabilities, err := adapter.Capabilities(ctx)
	if err != nil {
		add("Provider", "capabilities", doctorFail, err.Error())
		return
	}
	add("Provider", "capabilities", doctorOK, summarizeCapabilities(capabilities))
	if resolved != nil {
		if reasons := routing.ValidateCapabilities(resolved.Job, capabilities); len(reasons) > 0 {
			add("Provider", "config_support", doctorFail, strings.Join(reasons, "; "))
		} else {
			add("Provider", "config_support", doctorOK, "workload is supported by provider capabilities")
		}
	}
}

func addModalDiagnostics(ctx context.Context, add func(string, string, doctorStatus, string)) {
	modalPath, err := exec.LookPath("modal")
	if err != nil {
		add("Modal", "cli", doctorFail, "modal CLI not found on PATH; install with: python3 -m venv .venv && source .venv/bin/activate && pip install modal")
	} else {
		add("Modal", "cli", doctorOK, modalPath)
		if err := runCommand(ctx, "modal", "token", "info"); err != nil {
			add("Modal", "auth", doctorFail, "modal token info failed; run modal setup or set MODAL_TOKEN_ID/MODAL_TOKEN_SECRET")
		} else {
			add("Modal", "auth", doctorOK, "modal token info succeeded")
		}
	}
	python, err := findPython()
	if err != nil {
		add("Modal", "python_sdk", doctorFail, "python3 not found on PATH")
	} else if err := runCommand(ctx, python, "-c", "import modal"); err != nil {
		add("Modal", "python_sdk", doctorFail, "Python modal package is not importable; install with: pip install modal")
	} else {
		add("Modal", "python_sdk", doctorOK, "Python modal package import succeeded")
	}
	if os.Getenv("MODAL_TOKEN_ID") != "" && os.Getenv("MODAL_TOKEN_SECRET") != "" {
		add("Modal", "token_env", doctorOK, "MODAL_TOKEN_ID and MODAL_TOKEN_SECRET are set")
	} else {
		add("Modal", "token_env", doctorWarn, "MODAL_TOKEN_ID/MODAL_TOKEN_SECRET not both set; Modal may still use ~/.modal.toml")
	}
}

func addPathReadiness(add func(string, string, doctorStatus, string), section string, name string, path string) {
	info, err := os.Stat(path)
	if err == nil {
		if !info.IsDir() {
			add(section, name, doctorFail, fmt.Sprintf("%s exists but is not a directory", path))
			return
		}
		if err := checkWritableDir(path); err != nil {
			add(section, name, doctorFail, err.Error())
			return
		}
		add(section, name, doctorOK, path)
		return
	}
	if !errors.Is(err, os.ErrNotExist) {
		add(section, name, doctorFail, err.Error())
		return
	}
	parent, err := nearestWritableAncestor(path)
	if err != nil {
		add(section, name, doctorFail, fmt.Sprintf("%s does not exist and no writable parent was found: %v", path, err))
		return
	}
	add(section, name, doctorWarn, fmt.Sprintf("%s does not exist yet; nearest writable parent is %s", path, parent))
}

func addCommandCheck(ctx context.Context, add func(string, string, doctorStatus, string), section string, name string, command string, args ...string) {
	path, err := exec.LookPath(command)
	if err != nil {
		add(section, name, doctorWarn, fmt.Sprintf("%s not found on PATH", command))
		return
	}
	if err := runCommand(ctx, command, args...); err != nil {
		add(section, name, doctorWarn, fmt.Sprintf("%s found at %s but check failed: %v", command, path, err))
		return
	}
	add(section, name, doctorOK, path)
}

func addPythonCheck(ctx context.Context, add func(string, string, doctorStatus, string), section string, name string) {
	python, err := findPython()
	if err != nil {
		add(section, name, doctorFail, "python3 not found on PATH")
		return
	}
	if err := runCommand(ctx, python, "--version"); err != nil {
		add(section, name, doctorFail, err.Error())
		return
	}
	add(section, name, doctorOK, python)
}

func addFileCheck(add func(string, string, doctorStatus, string), section string, name string, path string) {
	if strings.TrimSpace(path) == "" {
		add(section, name, doctorFail, "path is empty")
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		add(section, name, doctorFail, err.Error())
		return
	}
	if info.IsDir() {
		add(section, name, doctorFail, fmt.Sprintf("%s is a directory", path))
		return
	}
	add(section, name, doctorOK, path)
}

func addOutputPathCheck(add func(string, string, doctorStatus, string), path string) {
	if strings.TrimSpace(path) == "" {
		return
	}
	info, err := os.Stat(path)
	if err == nil && !info.IsDir() {
		add("Config", "outputs", doctorFail, fmt.Sprintf("%s exists but is not a directory", path))
		return
	}
	if err == nil {
		if err := checkWritableDir(path); err != nil {
			add("Config", "outputs", doctorFail, err.Error())
			return
		}
		add("Config", "outputs", doctorOK, path)
		return
	}
	parent, err := nearestWritableAncestor(path)
	if err != nil {
		add("Config", "outputs", doctorFail, fmt.Sprintf("%s does not exist and no writable parent was found: %v", path, err))
		return
	}
	add("Config", "outputs", doctorWarn, fmt.Sprintf("%s does not exist yet; nearest writable parent is %s", path, parent))
}

func checkWritableDir(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", path)
	}
	file, err := os.CreateTemp(path, ".cloudtune-doctor-*")
	if err != nil {
		return fmt.Errorf("%s is not writable: %w", path, err)
	}
	name := file.Name()
	if err := file.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	return os.Remove(name)
}

func nearestWritableAncestor(path string) (string, error) {
	current := filepath.Clean(filepath.Dir(path))
	for {
		info, err := os.Stat(current)
		if err == nil {
			if !info.IsDir() {
				return "", fmt.Errorf("%s exists but is not a directory", current)
			}
			if err := checkWritableDir(current); err != nil {
				return "", err
			}
			return current, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(current)
		if parent == current {
			return "", err
		}
		current = parent
	}
}

func findPython() (string, error) {
	if path, err := exec.LookPath("python3"); err == nil {
		return path, nil
	}
	return exec.LookPath("python")
}

func runCommand(ctx context.Context, command string, args ...string) error {
	commandCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(commandCtx, command, args...)
	if err := cmd.Run(); err != nil {
		if commandCtx.Err() != nil {
			return commandCtx.Err()
		}
		return err
	}
	return nil
}

func summarizeCapabilities(capabilities app.ProviderCapabilities) string {
	parts := []string{}
	if len(capabilities.WorkloadTypes) > 0 {
		parts = append(parts, "workloads="+strings.Join(capabilities.WorkloadTypes, ","))
	}
	if capabilities.LogMode != "" {
		parts = append(parts, "logs="+capabilities.LogMode)
	}
	parts = append(parts,
		fmt.Sprintf("artifacts=%t", capabilities.SupportsArtifacts),
		fmt.Sprintf("cancel=%t", capabilities.SupportsCancel),
		fmt.Sprintf("checkpoint_resume=%t", capabilities.SupportsCheckpointResume),
		fmt.Sprintf("remote=%t", capabilities.Remote),
	)
	return strings.Join(parts, " ")
}

func printDoctorReport(w io.Writer, report doctorReport) {
	fmt.Fprintln(w, "CloudTune doctor")
	fmt.Fprintln(w)
	grouped := map[string][]doctorCheck{}
	for _, check := range report.Checks {
		grouped[check.Section] = append(grouped[check.Section], check)
	}
	sections := make([]string, 0, len(grouped))
	for section := range grouped {
		sections = append(sections, section)
	}
	sort.Strings(sections)
	for _, section := range sections {
		fmt.Fprintf(w, "%s:\n", section)
		for _, check := range grouped[section] {
			message := check.Message
			if message == "" {
				message = string(check.Status)
			}
			fmt.Fprintf(w, "  %s: %s %s\n", check.Name, check.Status, message)
		}
		fmt.Fprintln(w)
	}
	result := "ready"
	if !report.Ready {
		result = "not ready"
	}
	if report.Provider != "" {
		fmt.Fprintf(w, "Result: provider %s is %s.\n", report.Provider, result)
		return
	}
	fmt.Fprintf(w, "Result: CloudTune is %s.\n", result)
}

func isModalAlias(provider string) bool {
	switch provider {
	case "modal", "modal-sandbox":
		return true
	default:
		return false
	}
}
