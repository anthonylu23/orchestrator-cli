package hyperbolic

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	pathpkg "path"
	"time"
)

type RemoteTarget struct {
	Host           string
	User           string
	PrivateKeyPath string
	ConnectTimeout time.Duration
}

type RemoteRunner interface {
	WaitForReady(ctx context.Context, target RemoteTarget, timeout time.Duration, sleep func(context.Context, time.Duration) error) error
	ReadFile(ctx context.Context, target RemoteTarget, path string) (string, error)
	WriteFile(ctx context.Context, target RemoteTarget, path string, content string, perm os.FileMode) error
	Run(ctx context.Context, target RemoteTarget, command string) (string, error)
}

type sshRemoteRunner struct{}

func newSSHRemoteRunner() RemoteRunner {
	return sshRemoteRunner{}
}

func (r sshRemoteRunner) WaitForReady(ctx context.Context, target RemoteTarget, timeout time.Duration, sleep func(context.Context, time.Duration) error) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if time.Now().After(deadline) {
			if lastErr != nil {
				return fmt.Errorf("ssh did not become ready before timeout: %w", lastErr)
			}
			return errors.New("ssh did not become ready before timeout")
		}
		if _, err := r.Run(ctx, target, "true"); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if err := sleep(ctx, 5*time.Second); err != nil {
			return err
		}
	}
}

func (r sshRemoteRunner) ReadFile(ctx context.Context, target RemoteTarget, path string) (string, error) {
	return r.Run(ctx, target, "cat "+shellQuote(path))
}

func (r sshRemoteRunner) WriteFile(ctx context.Context, target RemoteTarget, path string, content string, perm os.FileMode) error {
	dir := pathpkg.Dir(path)
	command := "mkdir -p " + shellQuote(dir) + " && cat > " + shellQuote(path) + " && chmod " + fmt.Sprintf("%04o", perm.Perm()) + " " + shellQuote(path)
	_, err := r.run(ctx, target, command, content)
	return err
}

func (r sshRemoteRunner) Run(ctx context.Context, target RemoteTarget, command string) (string, error) {
	return r.run(ctx, target, command, "")
}

func (r sshRemoteRunner) run(ctx context.Context, target RemoteTarget, command string, stdin string) (string, error) {
	args := []string{
		"-i", target.PrivateKeyPath,
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", fmt.Sprintf("ConnectTimeout=%d", int(target.ConnectTimeout.Seconds())),
		target.User + "@" + target.Host,
		command,
	}
	cmd := exec.CommandContext(ctx, "ssh", args...)
	if stdin != "" {
		cmd.Stdin = bytes.NewBufferString(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("ssh %s: %w: %s", target.Host, err, stderr.String())
	}
	return stdout.String(), nil
}
