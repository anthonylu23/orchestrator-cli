package hyperbolic

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/anthonylu23/switchboard-cli/internal/app"
	"github.com/anthonylu23/switchboard-cli/internal/event"
	"github.com/anthonylu23/switchboard-cli/internal/redact"
)

const remoteSwitchboardDir = "/tmp/switchboard"
const remoteLogsPath = remoteSwitchboardDir + "/logs.txt"
const remoteEventsPath = remoteSwitchboardDir + "/events.jsonl"
const remoteExitPath = remoteSwitchboardDir + "/exit.json"
const remoteCheckpointsDir = remoteSwitchboardDir + "/checkpoints"
const remoteEnvPath = remoteSwitchboardDir + "/container.env"
const remoteRegistryPasswordPath = remoteSwitchboardDir + "/registry-password"
const remoteRunScriptPath = "/tmp/switchboard-run.sh"

func runnerScript(req app.SubmitRequest, registryAuth RegistryAuth) string {
	env := map[string]string{}
	for key, value := range req.JobSpec.Env {
		env[key] = value
	}
	for key, value := range req.RuntimeEnv {
		env[key] = value
	}
	env["SWITCHBOARD_EVENTS_PATH"] = remoteEventsPath
	env["ORCHESTRATOR_EVENTS_PATH"] = remoteEventsPath
	env["SWITCHBOARD_CHECKPOINT_DIR"] = remoteCheckpointsDir
	env["ORCHESTRATOR_CHECKPOINT_DIR"] = remoteCheckpointsDir

	var dockerArgs []string
	dockerArgs = append(dockerArgs, "docker", "run", "--rm", "--gpus", "all", "--env-file", remoteEnvPath)
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	dockerArgs = append(dockerArgs, "-v", remoteSwitchboardDir+":"+remoteSwitchboardDir)
	if req.JobSpec.WorkDir != "" && req.JobSpec.WorkDir != "." {
		dockerArgs = append(dockerArgs, "-w", req.JobSpec.WorkDir)
	}
	dockerArgs = append(dockerArgs, req.JobSpec.Image)
	dockerArgs = append(dockerArgs, req.JobSpec.Command...)
	dockerArgs = append(dockerArgs, req.JobSpec.Args...)

	lines := []string{
		"#!/usr/bin/env bash",
		"set +e",
		"mkdir -p " + shellQuote(remoteCheckpointsDir),
		"touch " + shellQuote(remoteEventsPath),
		"rm -f " + shellQuote(remoteExitPath),
		"rm -f " + shellQuote(remoteEnvPath),
		"rm -f " + shellQuote(remoteRegistryPasswordPath),
		envFileScript(keys, env, remoteEnvPath),
		"chmod 0600 " + shellQuote(remoteEnvPath),
	}
	if registryAuth.Server != "" {
		lines = append(lines,
			"printf '%s' "+shellQuote(registryAuth.Password)+" > "+shellQuote(remoteRegistryPasswordPath),
			"chmod 0600 "+shellQuote(remoteRegistryPasswordPath),
		)
	}
	lines = append(lines, "{")
	if registryAuth.Server != "" {
		lines = append(lines, "  docker login "+shellQuote(registryAuth.Server)+" --username "+shellQuote(registryAuth.Username)+" --password-stdin < "+shellQuote(remoteRegistryPasswordPath))
	}
	lines = append(lines,
		"  echo 'hyperbolic switchboard job starting'",
		"  docker pull "+shellQuote(req.JobSpec.Image),
		"  "+shellCommand(dockerArgs),
		"  code=$?",
		"  if [ \"$code\" -eq 0 ]; then reason='completed'; else reason=\"container exited with code $code\"; fi",
		"  printf '{\"exit_code\":%d,\"exit_reason\":\"%s\"}\\n' \"$code\" \"$reason\" > "+shellQuote(remoteExitPath),
		"  rm -f "+shellQuote(remoteRegistryPasswordPath),
		"  exit \"$code\"",
		"} >> "+shellQuote(remoteLogsPath)+" 2>&1",
	)
	return strings.Join(lines, "\n")
}

func envFileScript(keys []string, env map[string]string, path string) string {
	var lines []string
	for _, key := range keys {
		lines = append(lines, "printf '%s\\n' "+shellQuote(fmt.Sprintf("%s=%s", key, env[key]))+" >> "+shellQuote(path))
	}
	return strings.Join(lines, "\n")
}

func appendNewLogContent(logFile io.Writer, eventFile io.Writer, content string, offset int, runID string, attemptID string, now time.Time, redactor redact.Redactor, stdout io.Writer, seenEvents map[string]bool) int {
	if offset > len(content) {
		offset = 0
	}
	if offset == len(content) {
		return offset
	}
	newContent, nextOffset := completeContent(content, offset)
	if newContent == "" {
		return offset
	}
	redactedContent := redactedLogContent(newContent, redactor)
	_, _ = io.WriteString(logFile, redactedContent)
	if stdout != nil {
		_, _ = io.WriteString(stdout, redactedContent)
	}
	scanner := bufio.NewScanner(strings.NewReader(newContent))
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		parsed := event.ParseLine(line, runID, attemptID, now)
		if parsed.Structured {
			if seenEvents[line] {
				continue
			}
			seenEvents[line] = true
			_ = event.WriteJSONL(eventFile, redactor.Event(parsed.Event))
		}
	}
	if err := scanner.Err(); err != nil {
		_, _ = fmt.Fprintf(logFile, "log scanner error: %s\n", redactor.String(err.Error()))
	}
	return nextOffset
}

func appendNewEventContent(eventFile io.Writer, content string, offset int, runID string, attemptID string, now time.Time, redactor redact.Redactor, seenEvents map[string]bool) int {
	if offset > len(content) {
		offset = 0
	}
	if offset == len(content) {
		return offset
	}
	newContent, nextOffset := completeContent(content, offset)
	if newContent == "" {
		return offset
	}
	scanner := bufio.NewScanner(strings.NewReader(newContent))
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := scanner.Text()
		parsed := event.ParseLine(line, runID, attemptID, now)
		if parsed.Structured {
			if seenEvents[line] {
				continue
			}
			seenEvents[line] = true
			_ = event.WriteJSONL(eventFile, redactor.Event(parsed.Event))
		}
	}
	return nextOffset
}

func completeContent(content string, offset int) (string, int) {
	if offset >= len(content) {
		return "", offset
	}
	next := content[offset:]
	if strings.HasSuffix(next, "\n") {
		return next, len(content)
	}
	lastNewline := strings.LastIndex(next, "\n")
	if lastNewline < 0 {
		return "", offset
	}
	end := offset + lastNewline + 1
	return content[offset:end], end
}

func redactedLogContent(content string, redactor redact.Redactor) string {
	var out strings.Builder
	for _, part := range strings.SplitAfter(content, "\n") {
		if part == "" {
			continue
		}
		if strings.HasSuffix(part, "\n") {
			line := strings.TrimSuffix(part, "\n")
			out.WriteString(redactor.Line(line))
			out.WriteString("\n")
			continue
		}
		out.WriteString(redactor.Line(part))
	}
	return out.String()
}

func parseExit(content string) (int, string, error) {
	var out struct {
		ExitCode   int    `json:"exit_code"`
		ExitReason string `json:"exit_reason"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(content)), &out); err != nil {
		return 1, "", err
	}
	if out.ExitReason == "" {
		out.ExitReason = "completed"
		if out.ExitCode != 0 {
			out.ExitReason = fmt.Sprintf("container exited with code %d", out.ExitCode)
		}
	}
	return out.ExitCode, out.ExitReason, nil
}

func shellCommand(args []string) string {
	quoted := make([]string, 0, len(args))
	for _, arg := range args {
		quoted = append(quoted, shellQuote(arg))
	}
	return strings.Join(quoted, " ")
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
