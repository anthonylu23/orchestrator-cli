package state

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/anthonylu23/switchboard-cli/internal/app"
	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

func Open(path string) (*Store, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	if _, err := db.ExecContext(context.Background(), `PRAGMA busy_timeout = 5000`); err != nil {
		_ = db.Close()
		return nil, err
	}
	store := &Store{db: db}
	if err := store.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS runs (
  id TEXT PRIMARY KEY,
  job_name TEXT NOT NULL,
  script TEXT NOT NULL,
  image TEXT NOT NULL DEFAULT '',
  provider TEXT NOT NULL,
  state TEXT NOT NULL,
  started_at TEXT NOT NULL,
  ended_at TEXT,
  exit_code INTEGER NOT NULL DEFAULT 0,
  error TEXT NOT NULL DEFAULT ''
);
CREATE TABLE IF NOT EXISTS attempts (
  id TEXT PRIMARY KEY,
  run_id TEXT NOT NULL,
  provider TEXT NOT NULL,
  state TEXT NOT NULL,
  started_at TEXT NOT NULL,
  ended_at TEXT,
  exit_code INTEGER NOT NULL DEFAULT 0,
  exit_reason TEXT NOT NULL DEFAULT '',
  provider_ref TEXT NOT NULL DEFAULT '',
  FOREIGN KEY(run_id) REFERENCES runs(id)
);
CREATE TABLE IF NOT EXISTS routing_decisions (
  run_id TEXT PRIMARY KEY,
  selected_provider TEXT NOT NULL,
  objective TEXT NOT NULL,
  selection_reason TEXT NOT NULL,
  eligible_json TEXT NOT NULL,
  rejected_json TEXT NOT NULL,
  selected_hardware_json TEXT NOT NULL DEFAULT '',
  eligible_hardware_json TEXT NOT NULL DEFAULT '',
  rejected_hardware_json TEXT NOT NULL DEFAULT '',
  estimated_required_vram_gb REAL,
  estimated_runtime_sec REAL,
  estimated_total_cost_usd REAL,
  confidence TEXT NOT NULL DEFAULT '',
  FOREIGN KEY(run_id) REFERENCES runs(id)
);
CREATE TABLE IF NOT EXISTS provider_resources (
  id TEXT PRIMARY KEY,
  run_id TEXT NOT NULL,
  attempt_id TEXT NOT NULL,
  provider TEXT NOT NULL,
  kind TEXT NOT NULL,
  external_id TEXT NOT NULL,
  provider_ref TEXT NOT NULL,
  region TEXT NOT NULL DEFAULT '',
  project_or_account TEXT NOT NULL DEFAULT '',
  state TEXT NOT NULL,
  created_by_switchboard INTEGER NOT NULL DEFAULT 1,
  cleanup_policy TEXT NOT NULL DEFAULT 'never',
  metadata_json TEXT NOT NULL DEFAULT '{}',
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  last_observed_at TEXT,
  FOREIGN KEY(run_id) REFERENCES runs(id),
  FOREIGN KEY(attempt_id) REFERENCES attempts(id)
);
	`); err != nil {
		return err
	}
	if err := s.addColumnIfMissing(ctx, "runs", "image", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	for _, column := range []struct {
		name string
		def  string
	}{
		{name: "resume_from_uri", def: "TEXT NOT NULL DEFAULT ''"},
		{name: "resume_from_step", def: "INTEGER"},
		{name: "estimated_hourly_usd", def: "REAL"},
		{name: "estimate_currency", def: "TEXT NOT NULL DEFAULT ''"},
	} {
		if err := s.addColumnIfMissing(ctx, "attempts", column.name, column.def); err != nil {
			return err
		}
	}
	for _, column := range []struct {
		name string
		def  string
	}{
		{name: "selected_hardware_json", def: "TEXT NOT NULL DEFAULT ''"},
		{name: "eligible_hardware_json", def: "TEXT NOT NULL DEFAULT ''"},
		{name: "rejected_hardware_json", def: "TEXT NOT NULL DEFAULT ''"},
		{name: "estimated_required_vram_gb", def: "REAL"},
		{name: "estimated_runtime_sec", def: "REAL"},
		{name: "estimated_total_cost_usd", def: "REAL"},
		{name: "confidence", def: "TEXT NOT NULL DEFAULT ''"},
	} {
		if err := s.addColumnIfMissing(ctx, "routing_decisions", column.name, column.def); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) addColumnIfMissing(ctx context.Context, table string, column string, definition string) error {
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf("PRAGMA table_info(%s)", table))
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, columnType string
		var notNull int
		var defaultValue interface{}
		var primaryKey int
		if err := rows.Scan(&cid, &name, &columnType, &notNull, &defaultValue, &primaryKey); err != nil {
			return err
		}
		if strings.EqualFold(name, column) {
			return rows.Err()
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, fmt.Sprintf("ALTER TABLE %s ADD COLUMN %s %s", table, column, definition))
	return err
}

func (s *Store) CreateRun(ctx context.Context, run app.Run) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO runs (id, job_name, script, image, provider, state, started_at, exit_code, error)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		run.ID, run.JobName, run.Script, run.Image, run.Provider, run.State, run.StartedAt.Format(time.RFC3339Nano), run.ExitCode, run.Error)
	return err
}

func (s *Store) CreateAttempt(ctx context.Context, attempt app.Attempt) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO attempts (
  id, run_id, provider, state, started_at, exit_code, exit_reason, provider_ref,
  resume_from_uri, resume_from_step, estimated_hourly_usd, estimate_currency
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		attempt.ID, attempt.RunID, attempt.Provider, attempt.State, attempt.StartedAt.Format(time.RFC3339Nano), attempt.ExitCode, attempt.ExitReason, attempt.ProviderRef,
		attempt.ResumeFromURI, attempt.ResumeFromStep, attempt.EstimatedHourlyUSD, attempt.EstimateCurrency)
	return err
}

func (s *Store) FinishRun(ctx context.Context, runID string, state app.RunState, exitCode int, message string, endedAt time.Time) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE runs SET state = ?, exit_code = ?, error = ?, ended_at = ? WHERE id = ?`,
		state, exitCode, message, endedAt.Format(time.RFC3339Nano), runID)
	return err
}

func (s *Store) FinishAttempt(ctx context.Context, attemptID string, state app.AttemptState, exitCode int, exitReason string, providerRef string, endedAt time.Time) error {
	_, err := s.db.ExecContext(ctx, `
UPDATE attempts SET state = ?, exit_code = ?, exit_reason = ?, provider_ref = ?, ended_at = ? WHERE id = ?`,
		state, exitCode, exitReason, providerRef, endedAt.Format(time.RFC3339Nano), attemptID)
	return err
}

func (s *Store) UpdateAttemptProviderRef(ctx context.Context, attemptID string, providerRef string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE attempts SET provider_ref = ? WHERE id = ?`, providerRef, attemptID)
	return err
}

func (s *Store) UpdateAttemptEstimate(ctx context.Context, attemptID string, estimate app.CostEstimate) error {
	_, err := s.db.ExecContext(ctx, `UPDATE attempts SET estimated_hourly_usd = ?, estimate_currency = ? WHERE id = ?`, estimate.HourlyUSD, estimate.Currency, attemptID)
	return err
}

type ProviderResourceFilter struct {
	RunID    string
	Provider string
}

func (s *Store) SaveProviderResource(ctx context.Context, resource app.ProviderResource) (app.ProviderResource, error) {
	now := time.Now().UTC()
	if resource.ID == "" {
		resource.ID = app.NewProviderResourceID()
	}
	if resource.CreatedAt.IsZero() {
		resource.CreatedAt = now
	}
	if resource.UpdatedAt.IsZero() {
		resource.UpdatedAt = now
	}
	metadataJSON, err := marshalResourceMetadata(resource.Metadata)
	if err != nil {
		return app.ProviderResource{}, err
	}
	_, err = s.db.ExecContext(ctx, `
	INSERT INTO provider_resources (
	  id, run_id, attempt_id, provider, kind, external_id, provider_ref, region, project_or_account,
	  state, created_by_switchboard, cleanup_policy, metadata_json, created_at, updated_at, last_observed_at
	)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(id) DO UPDATE SET
	  run_id = excluded.run_id,
	  attempt_id = excluded.attempt_id,
	  provider = excluded.provider,
	  kind = excluded.kind,
	  external_id = excluded.external_id,
	  provider_ref = excluded.provider_ref,
	  region = excluded.region,
	  project_or_account = excluded.project_or_account,
	  state = excluded.state,
	  created_by_switchboard = excluded.created_by_switchboard,
	  cleanup_policy = excluded.cleanup_policy,
	  metadata_json = excluded.metadata_json,
	  updated_at = excluded.updated_at,
	  last_observed_at = excluded.last_observed_at`,
		resource.ID, resource.RunID, resource.AttemptID, resource.Provider, resource.Kind, resource.ExternalID, resource.ProviderRef,
		resource.Region, resource.ProjectOrAccount, resource.State, boolInt(resource.CreatedBySwitchboard), resource.CleanupPolicy,
		metadataJSON, resource.CreatedAt.Format(time.RFC3339Nano), resource.UpdatedAt.Format(time.RFC3339Nano), optionalTimeString(resource.LastObservedAt))
	if err != nil {
		return app.ProviderResource{}, err
	}
	return resource, nil
}

func (s *Store) ProviderResources(ctx context.Context, filter ProviderResourceFilter) ([]app.ProviderResource, error) {
	var clauses []string
	var args []interface{}
	if filter.RunID != "" {
		clauses = append(clauses, "run_id = ?")
		args = append(args, filter.RunID)
	}
	if filter.Provider != "" {
		clauses = append(clauses, "provider = ?")
		args = append(args, filter.Provider)
	}
	query := `
	SELECT id, run_id, attempt_id, provider, kind, external_id, provider_ref, region, project_or_account,
	  state, created_by_switchboard, cleanup_policy, metadata_json, created_at, updated_at, COALESCE(last_observed_at, '')
	FROM provider_resources`
	if len(clauses) > 0 {
		query += " WHERE " + strings.Join(clauses, " AND ")
	}
	query += " ORDER BY created_at, id"
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	resources := []app.ProviderResource{}
	for rows.Next() {
		resource, err := scanProviderResource(rows)
		if err != nil {
			return nil, err
		}
		resources = append(resources, resource)
	}
	return resources, rows.Err()
}

func (s *Store) ProviderResourcesByRun(ctx context.Context, runID string) ([]app.ProviderResource, error) {
	return s.ProviderResources(ctx, ProviderResourceFilter{RunID: runID})
}

func (s *Store) SaveRoutingDecision(ctx context.Context, decision app.RoutingDecision) error {
	eligible, err := json.Marshal(decision.EligibleProviders)
	if err != nil {
		return err
	}
	rejected, err := json.Marshal(decision.RejectedProviders)
	if err != nil {
		return err
	}
	selectedHardware, err := json.Marshal(decision.SelectedHardware)
	if err != nil {
		return err
	}
	eligibleHardware, err := json.Marshal(decision.EligibleHardware)
	if err != nil {
		return err
	}
	rejectedHardware, err := json.Marshal(decision.RejectedHardware)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO routing_decisions (
  run_id, selected_provider, objective, selection_reason, eligible_json, rejected_json,
  selected_hardware_json, eligible_hardware_json, rejected_hardware_json,
  estimated_required_vram_gb, estimated_runtime_sec, estimated_total_cost_usd, confidence
)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(run_id) DO UPDATE SET
  selected_provider = excluded.selected_provider,
  objective = excluded.objective,
  selection_reason = excluded.selection_reason,
  eligible_json = excluded.eligible_json,
  rejected_json = excluded.rejected_json,
  selected_hardware_json = excluded.selected_hardware_json,
  eligible_hardware_json = excluded.eligible_hardware_json,
  rejected_hardware_json = excluded.rejected_hardware_json,
  estimated_required_vram_gb = excluded.estimated_required_vram_gb,
  estimated_runtime_sec = excluded.estimated_runtime_sec,
  estimated_total_cost_usd = excluded.estimated_total_cost_usd,
  confidence = excluded.confidence`,
		decision.RunID, decision.SelectedProvider, decision.Objective, decision.SelectionReason, string(eligible), string(rejected),
		stringOrEmptyHardware(decision.SelectedHardware, string(selectedHardware)), string(eligibleHardware), string(rejectedHardware),
		decision.EstimatedRequiredVRAMGB, decision.EstimatedRuntimeSeconds, decision.EstimatedTotalCostUSD, decision.Confidence)
	return err
}

func (s *Store) GetRoutingDecision(ctx context.Context, runID string) (app.RoutingDecision, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT run_id, selected_provider, objective, selection_reason, eligible_json, rejected_json,
  selected_hardware_json, eligible_hardware_json, rejected_hardware_json,
  estimated_required_vram_gb, estimated_runtime_sec, estimated_total_cost_usd, confidence
FROM routing_decisions WHERE run_id = ?`, runID)
	var decision app.RoutingDecision
	var eligible, rejected, selectedHardware, eligibleHardware, rejectedHardware string
	var requiredVRAM, runtimeSeconds, totalCost sql.NullFloat64
	if err := row.Scan(&decision.RunID, &decision.SelectedProvider, &decision.Objective, &decision.SelectionReason, &eligible, &rejected,
		&selectedHardware, &eligibleHardware, &rejectedHardware, &requiredVRAM, &runtimeSeconds, &totalCost, &decision.Confidence); err != nil {
		if err == sql.ErrNoRows {
			return app.RoutingDecision{}, fmt.Errorf("routing decision for run %q not found", runID)
		}
		return app.RoutingDecision{}, err
	}
	if err := json.Unmarshal([]byte(eligible), &decision.EligibleProviders); err != nil {
		return app.RoutingDecision{}, err
	}
	if err := json.Unmarshal([]byte(rejected), &decision.RejectedProviders); err != nil {
		return app.RoutingDecision{}, err
	}
	if selectedHardware != "" {
		var hardware app.HardwareSelection
		if err := json.Unmarshal([]byte(selectedHardware), &hardware); err != nil {
			return app.RoutingDecision{}, err
		}
		decision.SelectedHardware = &hardware
	}
	if eligibleHardware != "" {
		if err := json.Unmarshal([]byte(eligibleHardware), &decision.EligibleHardware); err != nil {
			return app.RoutingDecision{}, err
		}
	}
	if rejectedHardware != "" {
		if err := json.Unmarshal([]byte(rejectedHardware), &decision.RejectedHardware); err != nil {
			return app.RoutingDecision{}, err
		}
	}
	if requiredVRAM.Valid {
		value := requiredVRAM.Float64
		decision.EstimatedRequiredVRAMGB = &value
	}
	if runtimeSeconds.Valid {
		value := runtimeSeconds.Float64
		decision.EstimatedRuntimeSeconds = &value
	}
	if totalCost.Valid {
		value := totalCost.Float64
		decision.EstimatedTotalCostUSD = &value
	}
	return decision, nil
}

func stringOrEmptyHardware(selection *app.HardwareSelection, value string) string {
	if selection == nil {
		return ""
	}
	return value
}

func (s *Store) GetRun(ctx context.Context, runID string) (app.Run, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, job_name, script, image, provider, state, started_at, COALESCE(ended_at, ''), exit_code, error
FROM runs WHERE id = ?`, runID)
	var run app.Run
	var started, ended string
	if err := row.Scan(&run.ID, &run.JobName, &run.Script, &run.Image, &run.Provider, &run.State, &started, &ended, &run.ExitCode, &run.Error); err != nil {
		if err == sql.ErrNoRows {
			return app.Run{}, fmt.Errorf("run %q not found", runID)
		}
		return app.Run{}, err
	}
	run.StartedAt = mustParseTime(started)
	if ended != "" {
		run.EndedAt = mustParseTime(ended)
	}
	return run, nil
}

func (s *Store) AttemptsByRun(ctx context.Context, runID string) ([]app.Attempt, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, run_id, provider, state, started_at, COALESCE(ended_at, ''), exit_code, exit_reason, provider_ref,
  resume_from_uri, resume_from_step, estimated_hourly_usd, estimate_currency
FROM attempts WHERE run_id = ? ORDER BY started_at`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var attempts []app.Attempt
	for rows.Next() {
		var attempt app.Attempt
		var started, ended string
		var resumeFromStep sql.NullInt64
		var estimatedHourlyUSD sql.NullFloat64
		if err := rows.Scan(&attempt.ID, &attempt.RunID, &attempt.Provider, &attempt.State, &started, &ended, &attempt.ExitCode, &attempt.ExitReason, &attempt.ProviderRef,
			&attempt.ResumeFromURI, &resumeFromStep, &estimatedHourlyUSD, &attempt.EstimateCurrency); err != nil {
			return nil, err
		}
		attempt.StartedAt = mustParseTime(started)
		if ended != "" {
			attempt.EndedAt = mustParseTime(ended)
		}
		if resumeFromStep.Valid {
			step := resumeFromStep.Int64
			attempt.ResumeFromStep = &step
		}
		if estimatedHourlyUSD.Valid {
			hourlyUSD := estimatedHourlyUSD.Float64
			attempt.EstimatedHourlyUSD = &hourlyUSD
		}
		attempts = append(attempts, attempt)
	}
	return attempts, rows.Err()
}

type providerResourceScanner interface {
	Scan(dest ...interface{}) error
}

func scanProviderResource(scanner providerResourceScanner) (app.ProviderResource, error) {
	var resource app.ProviderResource
	var kind, state, cleanupPolicy string
	var metadataJSON string
	var created, updated, lastObserved string
	var createdBySwitchboard int
	if err := scanner.Scan(
		&resource.ID,
		&resource.RunID,
		&resource.AttemptID,
		&resource.Provider,
		&kind,
		&resource.ExternalID,
		&resource.ProviderRef,
		&resource.Region,
		&resource.ProjectOrAccount,
		&state,
		&createdBySwitchboard,
		&cleanupPolicy,
		&metadataJSON,
		&created,
		&updated,
		&lastObserved,
	); err != nil {
		return app.ProviderResource{}, err
	}
	resource.Kind = app.ProviderResourceKind(kind)
	resource.State = app.ProviderResourceState(state)
	resource.CreatedBySwitchboard = createdBySwitchboard != 0
	resource.CleanupPolicy = app.ProviderResourceCleanupPolicy(cleanupPolicy)
	if metadataJSON != "" {
		if err := json.Unmarshal([]byte(metadataJSON), &resource.Metadata); err != nil {
			return app.ProviderResource{}, err
		}
	}
	resource.CreatedAt = mustParseTime(created)
	resource.UpdatedAt = mustParseTime(updated)
	if lastObserved != "" {
		parsed := mustParseTime(lastObserved)
		resource.LastObservedAt = &parsed
	}
	return resource, nil
}

func marshalResourceMetadata(metadata map[string]string) (string, error) {
	if metadata == nil {
		metadata = map[string]string{}
	}
	content, err := json.Marshal(metadata)
	if err != nil {
		return "", err
	}
	return string(content), nil
}

func optionalTimeString(value *time.Time) interface{} {
	if value == nil || value.IsZero() {
		return nil
	}
	return value.UTC().Format(time.RFC3339Nano)
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func mustParseTime(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}
	}
	return parsed
}
