package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strconv"

	"github.com/anthonylu23/switchboard-cli/internal/app"
	"github.com/anthonylu23/switchboard-cli/internal/artifact"
	"github.com/anthonylu23/switchboard-cli/internal/state"
	"github.com/spf13/cobra"
)

type CompareReport struct {
	Left  ComparableRun `json:"left"`
	Right ComparableRun `json:"right"`
	Rows  []CompareRow  `json:"rows"`
}

type ComparableRun struct {
	RunID          string       `json:"run_id"`
	Provider       string       `json:"provider"`
	State          app.RunState `json:"state"`
	RuntimeSeconds float64      `json:"runtime_sec"`
	ArtifactCount  int          `json:"artifact_count"`
	ConfigHash     string       `json:"config_hash,omitempty"`
	DatasetSHA256  string       `json:"dataset_sha256,omitempty"`
	Accuracy       *float64     `json:"accuracy,omitempty"`
}

type CompareRow struct {
	Field string `json:"field"`
	Left  string `json:"left"`
	Right string `json:"right"`
	Match bool   `json:"match"`
}

func newCompareCommand(opts Options, home *string) *cobra.Command {
	var asJSON bool
	cmd := &cobra.Command{
		Use:   "compare <run-id> <run-id>",
		Short: "Compare two run records",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			resolvedHome, err := resolveHome(*home)
			if err != nil {
				return err
			}
			report, err := buildCompareReport(cmd.Context(), resolvedHome, args[0], args[1])
			if err != nil {
				return err
			}
			if asJSON {
				return json.NewEncoder(opts.Stdout).Encode(report)
			}
			fmt.Fprintf(opts.Stdout, "field\t%s\t%s\tmatch\n", report.Left.RunID, report.Right.RunID)
			for _, row := range report.Rows {
				fmt.Fprintf(opts.Stdout, "%s\t%s\t%s\t%t\n", row.Field, row.Left, row.Right, row.Match)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&asJSON, "json", false, "Print JSON")
	return cmd
}

func buildCompareReport(ctx context.Context, home string, leftRunID string, rightRunID string) (CompareReport, error) {
	store, err := state.Open(artifact.DBPath(home))
	if err != nil {
		return CompareReport{}, err
	}
	defer store.Close()
	left, err := loadComparableRun(ctx, store, home, leftRunID)
	if err != nil {
		return CompareReport{}, err
	}
	right, err := loadComparableRun(ctx, store, home, rightRunID)
	if err != nil {
		return CompareReport{}, err
	}
	rows := []CompareRow{
		compareRow("state", string(left.State), string(right.State)),
		compareRow("provider", left.Provider, right.Provider),
		compareRow("runtime_sec", formatFloat(left.RuntimeSeconds), formatFloat(right.RuntimeSeconds)),
		compareRow("artifact_count", strconv.Itoa(left.ArtifactCount), strconv.Itoa(right.ArtifactCount)),
		compareRow("config_hash", displayValue(left.ConfigHash), displayValue(right.ConfigHash)),
		compareRow("dataset_sha256", displayValue(left.DatasetSHA256), displayValue(right.DatasetSHA256)),
		compareRow("eval_accuracy", formatOptionalFloat(left.Accuracy), formatOptionalFloat(right.Accuracy)),
	}
	return CompareReport{Left: left, Right: right, Rows: rows}, nil
}

func loadComparableRun(ctx context.Context, store *state.Store, home string, runID string) (ComparableRun, error) {
	run, err := store.GetRun(ctx, runID)
	if err != nil {
		return ComparableRun{}, err
	}
	paths := artifact.ForRun(home, runID)
	summary, err := readSummary(paths.Summary)
	if err != nil {
		return ComparableRun{}, err
	}
	manifest, err := artifact.ReadManifest(paths.Manifest)
	if err != nil {
		return ComparableRun{}, err
	}
	evidence, err := artifact.ReadRunEvidence(paths.WorkloadManifest)
	if err != nil {
		return ComparableRun{}, err
	}
	out := ComparableRun{
		RunID:          run.ID,
		Provider:       run.Provider,
		State:          run.State,
		RuntimeSeconds: summary.RuntimeSeconds,
		ArtifactCount:  len(manifest.Artifacts),
		ConfigHash:     evidence.ConfigHash,
	}
	if evidence.Dataset != nil {
		out.DatasetSHA256 = evidence.Dataset.SHA256
	}
	if accuracy, ok := summary.FinalMetrics["accuracy"]; ok {
		value := accuracy
		out.Accuracy = &value
	}
	return out, nil
}

func readSummary(path string) (app.Summary, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return app.Summary{}, err
	}
	var summary app.Summary
	if err := json.Unmarshal(content, &summary); err != nil {
		return app.Summary{}, err
	}
	return summary, nil
}

func compareRow(field string, left string, right string) CompareRow {
	return CompareRow{Field: field, Left: left, Right: right, Match: left == right}
}

func displayValue(value string) string {
	if value == "" {
		return "-"
	}
	return value
}

func formatOptionalFloat(value *float64) string {
	if value == nil {
		return "-"
	}
	return formatFloat(*value)
}

func formatFloat(value float64) string {
	return strconv.FormatFloat(value, 'f', 6, 64)
}
