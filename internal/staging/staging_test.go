package staging

import (
	"testing"

	"github.com/anthonylu23/switchboard-cli/internal/app"
	"github.com/anthonylu23/switchboard-cli/internal/config"
)

func TestRuntimeEnvAddsSharedPrefixesAndInputURIs(t *testing.T) {
	env := RuntimeEnv(config.StagingConfig{
		CheckpointURIPrefix: "s3://bucket/switchboard/checkpoints/",
		DataURIPrefix:       "s3://bucket/switchboard/data",
	}, "r_123", []app.DataInput{
		{Name: "train-set", Source: "s3://bucket/raw/train.csv", Mount: "/workspace/data/train.csv", Mode: app.DataInputModeURI},
		{Name: "local", Source: "./data", Mount: "/workspace/data/local", Mode: app.DataInputModeBundle},
	})

	if env["SWITCHBOARD_CHECKPOINT_URI_PREFIX"] != "s3://bucket/switchboard/checkpoints/r_123/checkpoints" {
		t.Fatalf("checkpoint prefix = %q", env["SWITCHBOARD_CHECKPOINT_URI_PREFIX"])
	}
	if env["SWITCHBOARD_DATA_URI_PREFIX"] != "s3://bucket/switchboard/data/r_123/data" {
		t.Fatalf("data prefix = %q", env["SWITCHBOARD_DATA_URI_PREFIX"])
	}
	if env["SWITCHBOARD_DATA_TRAIN_SET_URI"] != "s3://bucket/raw/train.csv" {
		t.Fatalf("train uri = %q", env["SWITCHBOARD_DATA_TRAIN_SET_URI"])
	}
	if env["SWITCHBOARD_DATA_TRAIN_SET_MOUNT"] != "/workspace/data/train.csv" {
		t.Fatalf("train mount = %q", env["SWITCHBOARD_DATA_TRAIN_SET_MOUNT"])
	}
	if _, ok := env["SWITCHBOARD_DATA_LOCAL_URI"]; ok {
		t.Fatalf("bundled input should not get URI env: %#v", env)
	}
}
