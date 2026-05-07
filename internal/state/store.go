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
  FOREIGN KEY(run_id) REFERENCES runs(id)
);
`); err != nil {
		return err
	}
	for _, column := range []struct {
		table string
		name  string
		def   string
	}{
		{table: "runs", name: "workload_type", def: "TEXT NOT NULL DEFAULT ''"},
		{table: "attempts", name: "resume_from_uri", def: "TEXT NOT NULL DEFAULT ''"},
		{table: "attempts", name: "resume_from_step", def: "INTEGER"},
		{table: "attempts", name: "estimated_hourly_usd", def: "REAL"},
		{table: "attempts", name: "estimate_currency", def: "TEXT NOT NULL DEFAULT ''"},
	} {
		if err := s.addColumnIfMissing(ctx, column.table, column.name, column.def); err != nil {
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
	INSERT INTO runs (id, job_name, script, provider, workload_type, state, started_at, exit_code, error)
	VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		run.ID, run.JobName, run.Script, run.Provider, run.WorkloadType, run.State, run.StartedAt.Format(time.RFC3339Nano), run.ExitCode, run.Error)
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

func (s *Store) SaveRoutingDecision(ctx context.Context, decision app.RoutingDecision) error {
	eligible, err := json.Marshal(decision.EligibleProviders)
	if err != nil {
		return err
	}
	rejected, err := json.Marshal(decision.RejectedProviders)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `
INSERT INTO routing_decisions (run_id, selected_provider, objective, selection_reason, eligible_json, rejected_json)
VALUES (?, ?, ?, ?, ?, ?)
ON CONFLICT(run_id) DO UPDATE SET
  selected_provider = excluded.selected_provider,
  objective = excluded.objective,
  selection_reason = excluded.selection_reason,
  eligible_json = excluded.eligible_json,
  rejected_json = excluded.rejected_json`,
		decision.RunID, decision.SelectedProvider, decision.Objective, decision.SelectionReason, string(eligible), string(rejected))
	return err
}

func (s *Store) GetRoutingDecision(ctx context.Context, runID string) (app.RoutingDecision, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT run_id, selected_provider, objective, selection_reason, eligible_json, rejected_json
FROM routing_decisions WHERE run_id = ?`, runID)
	var decision app.RoutingDecision
	var eligible, rejected string
	if err := row.Scan(&decision.RunID, &decision.SelectedProvider, &decision.Objective, &decision.SelectionReason, &eligible, &rejected); err != nil {
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
	return decision, nil
}

func (s *Store) GetRun(ctx context.Context, runID string) (app.Run, error) {
	row := s.db.QueryRowContext(ctx, `
	SELECT id, job_name, script, provider, workload_type, state, started_at, COALESCE(ended_at, ''), exit_code, error
	FROM runs WHERE id = ?`, runID)
	var run app.Run
	var started, ended string
	if err := row.Scan(&run.ID, &run.JobName, &run.Script, &run.Provider, &run.WorkloadType, &run.State, &started, &ended, &run.ExitCode, &run.Error); err != nil {
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

func (s *Store) ListRuns(ctx context.Context, limit int) ([]app.Run, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
	SELECT id, job_name, script, provider, workload_type, state, started_at, COALESCE(ended_at, ''), exit_code, error
	FROM runs ORDER BY started_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var runs []app.Run
	for rows.Next() {
		var run app.Run
		var started, ended string
		if err := rows.Scan(&run.ID, &run.JobName, &run.Script, &run.Provider, &run.WorkloadType, &run.State, &started, &ended, &run.ExitCode, &run.Error); err != nil {
			return nil, err
		}
		run.StartedAt = mustParseTime(started)
		if ended != "" {
			run.EndedAt = mustParseTime(ended)
		}
		runs = append(runs, run)
	}
	return runs, rows.Err()
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

func mustParseTime(value string) time.Time {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}
	}
	return parsed
}
