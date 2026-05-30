package container_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestMaterializerCopiesFileURIInput(t *testing.T) {
	python := requirePython3(t)
	helper := helperPath(t)
	dir := t.TempDir()
	source := filepath.Join(dir, "train.csv")
	destination := filepath.Join(dir, "workspace", "data", "train.csv")
	if err := os.WriteFile(source, []byte("x,y\n1,2\n"), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}

	cmd := exec.Command(python, helper)
	cmd.Env = append(os.Environ(),
		"SWITCHBOARD_DATA_TRAIN_URI=file://"+source,
		"SWITCHBOARD_DATA_TRAIN_MOUNT="+destination,
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("materializer failed: %v\n%s", err, string(output))
	}
	content, err := os.ReadFile(destination)
	if err != nil {
		t.Fatalf("read destination: %v", err)
	}
	if string(content) != "x,y\n1,2\n" {
		t.Fatalf("destination content = %q", string(content))
	}
}

func TestMaterializerDryRunReportsObjectStoreInputs(t *testing.T) {
	python := requirePython3(t)
	helper := helperPath(t)
	cmd := exec.Command(python, helper, "--dry-run")
	cmd.Env = append(os.Environ(),
		"SWITCHBOARD_DATA_TRAIN_URI=s3://bucket/datasets/train/",
		"SWITCHBOARD_DATA_TRAIN_MOUNT=/workspace/data/train",
		"SWITCHBOARD_DATA_VALIDATION_URI=gs://bucket/validation.csv",
		"SWITCHBOARD_DATA_VALIDATION_MOUNT=/workspace/data/validation.csv",
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("dry run failed: %v\n%s", err, string(output))
	}
	var report struct {
		Inputs []struct {
			Key             string `json:"key"`
			Source          string `json:"source"`
			Mount           string `json:"mount"`
			DestinationKind string `json:"destination_kind"`
		} `json:"inputs"`
	}
	if err := json.Unmarshal(output, &report); err != nil {
		t.Fatalf("parse dry run: %v\n%s", err, string(output))
	}
	if len(report.Inputs) != 2 {
		t.Fatalf("inputs = %#v", report.Inputs)
	}
	if report.Inputs[0].Key != "TRAIN" || report.Inputs[0].DestinationKind != "directory" {
		t.Fatalf("first input = %#v", report.Inputs[0])
	}
	if report.Inputs[1].Key != "VALIDATION" || report.Inputs[1].DestinationKind != "file" {
		t.Fatalf("second input = %#v", report.Inputs[1])
	}
}

func requirePython3(t *testing.T) string {
	t.Helper()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available")
	}
	return python
}

func helperPath(t *testing.T) string {
	t.Helper()
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("get cwd: %v", err)
	}
	return filepath.Join(cwd, "switchboard_materialize_data.py")
}
