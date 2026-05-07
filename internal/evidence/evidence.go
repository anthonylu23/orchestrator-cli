package evidence

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/anthonylu23/switchboard-cli/internal/app"
)

const CloudTuneVersion = "dev"

type BuildOptions struct {
	Job               app.JobSpec
	RequestedProvider string
	ConfigPath        string
	ConfigHash        string
	Now               func() time.Time
}

func Build(ctx context.Context, opts BuildOptions) (app.RunEvidence, error) {
	now := time.Now
	if opts.Now != nil {
		now = opts.Now
	}
	workingDir, err := os.Getwd()
	if err != nil {
		return app.RunEvidence{}, fmt.Errorf("resolve working directory: %w", err)
	}
	entrypoint := opts.Job.Script
	if entrypoint != "" && !filepath.IsAbs(entrypoint) {
		entrypoint = filepath.Clean(entrypoint)
	}
	out := app.RunEvidence{
		Workload:          opts.Job.Workload,
		RequestedProvider: opts.RequestedProvider,
		ConfigPath:        opts.ConfigPath,
		ConfigHash:        opts.ConfigHash,
		WorkingDir:        workingDir,
		Entrypoint:        entrypoint,
		CloudTune:         CloudTuneVersion,
		GeneratedAt:       now().UTC(),
	}
	commit, dirty, gitErr := gitState(ctx)
	out.GitCommit = commit
	out.GitDirty = dirty
	if gitErr != nil {
		out.GitError = gitErr.Error()
	}
	if opts.Job.Workload.Dataset.Path != "" {
		fingerprint, err := FingerprintDataset(opts.Job.Workload.Dataset.Path)
		if err != nil {
			return app.RunEvidence{}, err
		}
		out.Dataset = &fingerprint
	}
	return out, nil
}

func FingerprintDataset(path string) (app.DatasetFingerprint, error) {
	cleanPath := filepath.Clean(path)
	file, err := os.Open(cleanPath)
	if err != nil {
		return app.DatasetFingerprint{}, fmt.Errorf("fingerprint dataset %q: %w", path, err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return app.DatasetFingerprint{}, fmt.Errorf("stat dataset %q: %w", path, err)
	}
	if info.IsDir() {
		return app.DatasetFingerprint{}, fmt.Errorf("fingerprint dataset %q: path is a directory", path)
	}
	hash := sha256.New()
	reader := bufio.NewReader(file)
	var records int64
	for {
		line, readErr := reader.ReadBytes('\n')
		if len(line) > 0 {
			if _, err := hash.Write(line); err != nil {
				return app.DatasetFingerprint{}, fmt.Errorf("hash dataset %q: %w", path, err)
			}
			if len(bytes.TrimSpace(line)) > 0 {
				records++
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return app.DatasetFingerprint{}, fmt.Errorf("read dataset %q: %w", path, readErr)
		}
	}
	return app.DatasetFingerprint{
		Path:       cleanPath,
		SHA256:     "sha256:" + hex.EncodeToString(hash.Sum(nil)),
		SizeBytes:  info.Size(),
		NumRecords: records,
	}, nil
}

func gitState(ctx context.Context) (string, bool, error) {
	commit, err := runGit(ctx, "rev-parse", "HEAD")
	if err != nil {
		return "", false, err
	}
	status, err := runGit(ctx, "status", "--porcelain")
	if err != nil {
		return commit, false, err
	}
	return strings.TrimSpace(commit), strings.TrimSpace(status) != "", nil
}

func runGit(ctx context.Context, args ...string) (string, error) {
	gitCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(gitCtx, "git", args...)
	out, err := cmd.Output()
	if gitCtx.Err() != nil {
		return "", fmt.Errorf("git %s timed out: %w", strings.Join(args, " "), gitCtx.Err())
	}
	if err != nil {
		return "", fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return string(out), nil
}
