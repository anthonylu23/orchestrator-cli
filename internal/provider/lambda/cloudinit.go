package lambda

import (
	"fmt"
	"sort"
	"strings"

	"github.com/anthonylu23/switchboard-cli/internal/app"
)

const remoteSwitchboardDir = "/tmp/switchboard"
const remoteLogsPath = remoteSwitchboardDir + "/logs.txt"
const remoteEventsPath = remoteSwitchboardDir + "/events.jsonl"
const remoteExitPath = remoteSwitchboardDir + "/exit.json"
const remoteCheckpointsDir = remoteSwitchboardDir + "/checkpoints"

func cloudInitUserData(req app.SubmitRequest) string {
	script := lambdaRunnerScript(req)
	var out strings.Builder
	out.WriteString("#cloud-config\n")
	out.WriteString("write_files:\n")
	out.WriteString("  - path: /tmp/switchboard-run.sh\n")
	out.WriteString("    permissions: '0755'\n")
	out.WriteString("    content: |\n")
	for _, line := range strings.Split(script, "\n") {
		out.WriteString("      ")
		out.WriteString(line)
		out.WriteString("\n")
	}
	out.WriteString("runcmd:\n")
	out.WriteString("  - [ bash, /tmp/switchboard-run.sh ]\n")
	return out.String()
}

func lambdaRunnerScript(req app.SubmitRequest) string {
	env := map[string]string{}
	for k, v := range req.RuntimeEnv {
		env[k] = v
	}
	for k, v := range req.JobSpec.Env {
		env[k] = v
	}
	env["SWITCHBOARD_EVENTS_PATH"] = remoteEventsPath
	env["ORCHESTRATOR_EVENTS_PATH"] = remoteEventsPath
	env["SWITCHBOARD_CHECKPOINT_DIR"] = remoteCheckpointsDir
	env["ORCHESTRATOR_CHECKPOINT_DIR"] = remoteCheckpointsDir

	var dockerArgs []string
	dockerArgs = append(dockerArgs, "docker", "run", "--rm", "--gpus", "all")
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		dockerArgs = append(dockerArgs, "-e", fmt.Sprintf("%s=%s", key, env[key]))
	}
	dockerArgs = append(dockerArgs, "-v", remoteSwitchboardDir+":"+remoteSwitchboardDir)
	if req.JobSpec.WorkDir != "" && req.JobSpec.WorkDir != "." {
		dockerArgs = append(dockerArgs, "-w", req.JobSpec.WorkDir)
	}
	dockerArgs = append(dockerArgs, req.JobSpec.Image)
	dockerArgs = append(dockerArgs, req.JobSpec.Command...)
	dockerArgs = append(dockerArgs, req.JobSpec.Args...)

	return strings.Join([]string{
		"#!/usr/bin/env bash",
		"set +e",
		"mkdir -p " + shellQuote(remoteCheckpointsDir),
		"touch " + shellQuote(remoteEventsPath),
		"rm -f " + shellQuote(remoteExitPath),
		"{",
		"  echo 'lambda switchboard job starting'",
		"  docker pull " + shellQuote(req.JobSpec.Image),
		"  " + shellCommand(dockerArgs),
		"  code=$?",
		"  if [ \"$code\" -eq 0 ]; then reason='completed'; else reason=\"container exited with code $code\"; fi",
		"  printf '{\"exit_code\":%d,\"exit_reason\":\"%s\"}\\n' \"$code\" \"$reason\" > " + shellQuote(remoteExitPath),
		"  exit \"$code\"",
		"} >> " + shellQuote(remoteLogsPath) + " 2>&1",
	}, "\n")
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
