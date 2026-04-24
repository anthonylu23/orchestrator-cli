package state

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/anthonylu23/orchestrator-cli/internal/app"
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
	_, err := s.db.ExecContext(ctx, `
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
`)
	return err
}

func (s *Store) CreateRun(ctx context.Context, run app.Run) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO runs (id, job_name, script, provider, state, started_at, exit_code, error)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		run.ID, run.JobName, run.Script, run.Provider, run.State, run.StartedAt.Format(time.RFC3339Nano), run.ExitCode, run.Error)
	return err
}

func (s *Store) CreateAttempt(ctx context.Context, attempt app.Attempt) error {
	_, err := s.db.ExecContext(ctx, `
INSERT INTO attempts (id, run_id, provider, state, started_at, exit_code, exit_reason, provider_ref)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		attempt.ID, attempt.RunID, attempt.Provider, attempt.State, attempt.StartedAt.Format(time.RFC3339Nano), attempt.ExitCode, attempt.ExitReason, attempt.ProviderRef)
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

func (s *Store) GetRun(ctx context.Context, runID string) (app.Run, error) {
	row := s.db.QueryRowContext(ctx, `
SELECT id, job_name, script, provider, state, started_at, COALESCE(ended_at, ''), exit_code, error
FROM runs WHERE id = ?`, runID)
	var run app.Run
	var started, ended string
	if err := row.Scan(&run.ID, &run.JobName, &run.Script, &run.Provider, &run.State, &started, &ended, &run.ExitCode, &run.Error); err != nil {
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
SELECT id, run_id, provider, state, started_at, COALESCE(ended_at, ''), exit_code, exit_reason, provider_ref
FROM attempts WHERE run_id = ? ORDER BY started_at`, runID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var attempts []app.Attempt
	for rows.Next() {
		var attempt app.Attempt
		var started, ended string
		if err := rows.Scan(&attempt.ID, &attempt.RunID, &attempt.Provider, &attempt.State, &started, &ended, &attempt.ExitCode, &attempt.ExitReason, &attempt.ProviderRef); err != nil {
			return nil, err
		}
		attempt.StartedAt = mustParseTime(started)
		if ended != "" {
			attempt.EndedAt = mustParseTime(ended)
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
