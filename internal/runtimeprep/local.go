package runtimeprep

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/anthonylu23/orchestrator-cli/internal/app"
)

const WorkspacePrefix = "/workspace"

type PreparedJob struct {
	Job       app.JobSpec
	Mounts    map[string]string
	Workspace string
}

func PrepareLocal(job app.JobSpec, manifest app.DataManifest, workspace string) (PreparedJob, error) {
	if workspace == "" {
		return PreparedJob{}, fmt.Errorf("workspace is required")
	}
	prepared := job
	prepared.WorkDir = workspace
	prepared.Script = absPath(job.Script)
	prepared.Data = append([]app.DataInput(nil), manifest.Inputs...)

	mounts := map[string]string{}
	for _, input := range manifest.Inputs {
		hostMount, err := HostPathForMount(workspace, input.Mount)
		if err != nil {
			return PreparedJob{}, err
		}
		mounts[input.Mount] = hostMount
		if input.Mode == app.DataInputModeBundle {
			if err := materializeBundle(input.Source, hostMount); err != nil {
				return PreparedJob{}, err
			}
		}
	}

	prepared.Args = rewriteStrings(prepared.Args, mounts)
	prepared.Env = rewriteEnv(prepared.Env, mounts)
	return PreparedJob{Job: prepared, Mounts: mounts, Workspace: workspace}, nil
}

func HostPathForMount(workspace string, mount string) (string, error) {
	if mount == "" {
		return "", fmt.Errorf("mount path is required")
	}
	clean := filepath.Clean(filepath.FromSlash(mount))
	prefix := filepath.Clean(filepath.FromSlash(WorkspacePrefix))
	if clean != prefix && !strings.HasPrefix(clean, prefix+string(os.PathSeparator)) {
		return "", fmt.Errorf("mount %q must be under %s", mount, WorkspacePrefix)
	}
	rel, err := filepath.Rel(prefix, clean)
	if err != nil {
		return "", err
	}
	if rel == "." {
		return workspace, nil
	}
	if strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("mount %q must be under %s", mount, WorkspacePrefix)
	}
	return filepath.Join(workspace, rel), nil
}

func RewriteValue(value string, mounts map[string]string) string {
	out := value
	keys := make([]string, 0, len(mounts))
	for k := range mounts {
		keys = append(keys, k)
	}
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if len(keys[j]) > len(keys[i]) {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	for _, mount := range keys {
		out = strings.ReplaceAll(out, mount, mounts[mount])
	}
	return out
}

func rewriteStrings(values []string, mounts map[string]string) []string {
	out := make([]string, len(values))
	for i, value := range values {
		out[i] = RewriteValue(value, mounts)
	}
	return out
}

func rewriteEnv(env map[string]string, mounts map[string]string) map[string]string {
	if env == nil {
		return nil
	}
	out := make(map[string]string, len(env))
	for k, value := range env {
		out[k] = RewriteValue(value, mounts)
	}
	return out
}

func materializeBundle(source string, dest string) error {
	info, err := os.Stat(source)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return copyDir(source, dest)
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	return copyFile(source, dest, info.Mode())
}

func copyDir(source string, dest string) error {
	return filepath.WalkDir(source, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dest, rel)
		info, err := d.Info()
		if err != nil {
			return err
		}
		if d.IsDir() {
			return os.MkdirAll(target, info.Mode())
		}
		return copyFile(path, target, info.Mode())
	})
}

func copyFile(source string, dest string, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func absPath(path string) string {
	if path == "" || filepath.IsAbs(path) {
		return path
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	return abs
}
