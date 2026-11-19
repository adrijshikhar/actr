package history

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/adrijshikhar/actr/internal/config"
	"github.com/adrijshikhar/actr/internal/monitor"
	_ "modernc.org/sqlite"
)

var (
	_ Store    = (*SQLiteStore)(nil)
	_ LogStore = (*SQLiteStore)(nil)
	_ Pruner   = (*SQLiteStore)(nil)
)

var (
	// ErrRunNotFound indicates that a requested run was not found in the SQLite store.
	ErrRunNotFound = errors.New("history run not found")
	// ErrStepNotFound indicates that a requested step was not found in the SQLite store.
	ErrStepNotFound = errors.New("history step not found")
)

// SQLiteStore provides pure-Go SQLite persistence for run metadata and step logs.
type SQLiteStore struct {
	db *sql.DB
	mu sync.RWMutex
}

// NewSQLiteStore initializes a pure-Go SQLite storage engine at dbPath.
// Parent directories are created automatically if they do not exist.
func NewSQLiteStore(dbPath string) (*SQLiteStore, error) {
	if dbPath == "" {
		return nil, errors.New("dbPath cannot be empty")
	}

	if err := os.MkdirAll(filepath.Dir(dbPath), 0700); err != nil {
		return nil, fmt.Errorf("failed to create sqlite directory %s: %w", filepath.Dir(dbPath), err)
	}

	dsn := fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=foreign_keys(ON)", dbPath)
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("failed to open sqlite database at %s: %w", dbPath, err)
	}

	// Set single-connection pool to serialize writes and eliminate SQLITE_BUSY concurrency conflicts.
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)

	store := &SQLiteStore{
		db: db,
	}

	if err := store.initSchema(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("failed to initialize sqlite schema: %w", err)
	}

	return store, nil
}

func (s *SQLiteStore) initSchema() error {
	ctx := context.Background()

	// Explicitly enforce foreign key constraints
	if _, err := s.db.ExecContext(ctx, "PRAGMA foreign_keys = ON;"); err != nil {
		return fmt.Errorf("enable foreign keys: %w", err)
	}

	schema := `
	CREATE TABLE IF NOT EXISTS runs (
		id TEXT PRIMARY KEY,
		repo_alias TEXT NOT NULL DEFAULT '',
		repo_url TEXT NOT NULL DEFAULT '',
		name TEXT NOT NULL DEFAULT '',
		workflow TEXT NOT NULL DEFAULT '',
		owner TEXT NOT NULL DEFAULT '',
		repo TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL DEFAULT '',
		started_at DATETIME NOT NULL,
		ended_at DATETIME,
		duration_sec INTEGER NOT NULL DEFAULT 0,
		driver TEXT NOT NULL DEFAULT '',
		peak_cpu_percent REAL NOT NULL DEFAULT 0,
		peak_rss_bytes INTEGER NOT NULL DEFAULT 0,
		total_net_rx_bytes INTEGER NOT NULL DEFAULT 0,
		total_net_tx_bytes INTEGER NOT NULL DEFAULT 0,
		total_disk_write_bytes INTEGER NOT NULL DEFAULT 0,
		step_count INTEGER NOT NULL DEFAULT 0,
		created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

	CREATE TABLE IF NOT EXISTS steps (
		id TEXT NOT NULL,
		run_id TEXT NOT NULL,
		step_index INTEGER NOT NULL DEFAULT 0,
		name TEXT NOT NULL DEFAULT '',
		status TEXT NOT NULL DEFAULT '',
		started_at DATETIME,
		ended_at DATETIME,
		duration_sec INTEGER NOT NULL DEFAULT 0,
		exit_code INTEGER NOT NULL DEFAULT 0,
		PRIMARY KEY (run_id, id),
		FOREIGN KEY (run_id) REFERENCES runs(id) ON DELETE CASCADE
	);

	CREATE TABLE IF NOT EXISTS step_logs (
		run_id TEXT NOT NULL,
		step_id TEXT NOT NULL,
		line_num INTEGER NOT NULL,
		log_line TEXT NOT NULL,
		PRIMARY KEY (run_id, step_id, line_num),
		FOREIGN KEY (run_id, step_id) REFERENCES steps(run_id, id) ON DELETE CASCADE
	);

	CREATE INDEX IF NOT EXISTS idx_runs_repo_started ON runs(repo_alias, started_at DESC);
	CREATE INDEX IF NOT EXISTS idx_runs_started ON runs(started_at DESC);
	CREATE INDEX IF NOT EXISTS idx_step_logs_lookup ON step_logs(run_id, step_id, line_num ASC);
	`

	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("exec schema ddl: %w", err)
	}

	return nil
}

// Close closes the underlying SQLite database connection.
func (s *SQLiteStore) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

// RecordRun inserts or updates the full run record and associated step records.
func (s *SQLiteStore) RecordRun(meta *monitor.RunMetadata) error {
	if meta == nil || meta.ID == "" {
		return errors.New("empty run metadata or ID")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	ctx := context.Background()

	var endedAt sql.NullTime
	if !meta.EndedAt.IsZero() {
		endedAt = sql.NullTime{Time: meta.EndedAt, Valid: true}
	}

	stepCount := len(meta.Steps)

	_, err := s.db.ExecContext(ctx, `
		INSERT INTO runs (
			id, repo_alias, repo_url, name, workflow, owner, repo, status,
			started_at, ended_at, duration_sec, driver, peak_cpu_percent,
			peak_rss_bytes, total_net_rx_bytes, total_net_tx_bytes,
			total_disk_write_bytes, step_count
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			repo_alias = excluded.repo_alias,
			repo_url = excluded.repo_url,
			name = excluded.name,
			workflow = excluded.workflow,
			owner = excluded.owner,
			repo = excluded.repo,
			status = excluded.status,
			started_at = excluded.started_at,
			ended_at = excluded.ended_at,
			duration_sec = excluded.duration_sec,
			driver = excluded.driver,
			peak_cpu_percent = excluded.peak_cpu_percent,
			peak_rss_bytes = excluded.peak_rss_bytes,
			total_net_rx_bytes = excluded.total_net_rx_bytes,
			total_net_tx_bytes = excluded.total_net_tx_bytes,
			total_disk_write_bytes = excluded.total_disk_write_bytes,
			step_count = excluded.step_count
	`,
		meta.ID, meta.RepoAlias, meta.RepoURL, meta.Name, meta.Workflow, meta.Owner, meta.Repo, meta.Status,
		meta.StartedAt, endedAt, meta.DurationSec, meta.Driver, meta.MetricsSummary.PeakCPUPercent,
		meta.MetricsSummary.PeakRSSBytes, meta.MetricsSummary.TotalNetRxBytes, meta.MetricsSummary.TotalNetTxBytes,
		meta.MetricsSummary.TotalDiskWrittenBytes, stepCount,
	)
	if err != nil {
		return fmt.Errorf("upsert run %s: %w", meta.ID, err)
	}

	for i, st := range meta.Steps {
		if st == nil {
			continue
		}
		var stStartedAt, stEndedAt sql.NullTime
		if !st.StartedAt.IsZero() {
			stStartedAt = sql.NullTime{Time: st.StartedAt, Valid: true}
		}
		if !st.EndedAt.IsZero() {
			stEndedAt = sql.NullTime{Time: st.EndedAt, Valid: true}
		}
		stepIndex := st.Number
		if stepIndex == 0 {
			stepIndex = i + 1
		}

		_, err := s.db.ExecContext(ctx, `
			INSERT INTO steps (
				id, run_id, step_index, name, status, started_at, ended_at, duration_sec, exit_code
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(run_id, id) DO UPDATE SET
				step_index = excluded.step_index,
				name = excluded.name,
				status = excluded.status,
				started_at = excluded.started_at,
				ended_at = excluded.ended_at,
				duration_sec = excluded.duration_sec,
				exit_code = excluded.exit_code
		`,
			st.ID, meta.ID, stepIndex, st.Name, st.Status, stStartedAt, stEndedAt, st.DurationSec, st.ExitCode,
		)
		if err != nil {
			return fmt.Errorf("upsert step %s for run %s: %w", st.ID, meta.ID, err)
		}
	}

	return nil
}

// UpdateRun updates an existing run metadata record and steps.
func (s *SQLiteStore) UpdateRun(meta *monitor.RunMetadata) error {
	return s.RecordRun(meta)
}

// GetRun reads and returns the full RunMetadata and StepMetadata for the given jobID.
func (s *SQLiteStore) GetRun(jobID string) (*monitor.RunMetadata, error) {
	if jobID == "" {
		return nil, errors.New("empty job ID")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()

	ctx := context.Background()

	var (
		meta      monitor.RunMetadata
		endedAt   sql.NullTime
		peakCPU   float64
		peakRSS   uint64
		netRx     uint64
		netTx     uint64
		diskWrite uint64
	)

	err := s.db.QueryRowContext(ctx, `
		SELECT id, repo_alias, repo_url, name, workflow, owner, repo, status,
		       started_at, ended_at, duration_sec, driver, peak_cpu_percent,
		       peak_rss_bytes, total_net_rx_bytes, total_net_tx_bytes, total_disk_write_bytes
		FROM runs
		WHERE id = ?
	`, jobID).Scan(
		&meta.ID,
		&meta.RepoAlias,
		&meta.RepoURL,
		&meta.Name,
		&meta.Workflow,
		&meta.Owner,
		&meta.Repo,
		&meta.Status,
		&meta.StartedAt,
		&endedAt,
		&meta.DurationSec,
		&meta.Driver,
		&peakCPU,
		&peakRSS,
		&netRx,
		&netTx,
		&diskWrite,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ErrRunNotFound
		}
		return nil, fmt.Errorf("query run %s: %w", jobID, err)
	}

	if endedAt.Valid {
		meta.EndedAt = endedAt.Time
	}

	meta.MetricsSummary = monitor.RunMetricsSummary{
		PeakCPUPercent:        peakCPU,
		PeakRSSBytes:          peakRSS,
		TotalNetRxBytes:       netRx,
		TotalNetTxBytes:       netTx,
		TotalDiskWrittenBytes: diskWrite,
	}

	// Fetch steps
	stepRows, err := s.db.QueryContext(ctx, `
		SELECT id, step_index, name, status, started_at, ended_at, duration_sec, exit_code
		FROM steps
		WHERE run_id = ?
		ORDER BY step_index ASC, started_at ASC
	`, jobID)
	if err != nil {
		return nil, fmt.Errorf("query steps for run %s: %w", jobID, err)
	}
	defer func() { _ = stepRows.Close() }()

	steps := make([]*monitor.StepMetadata, 0)
	for stepRows.Next() {
		var (
			st          monitor.StepMetadata
			stStartedAt sql.NullTime
			stEndedAt   sql.NullTime
		)
		if err := stepRows.Scan(&st.ID, &st.Number, &st.Name, &st.Status, &stStartedAt, &stEndedAt, &st.DurationSec, &st.ExitCode); err != nil {
			return nil, fmt.Errorf("scan step: %w", err)
		}
		if stStartedAt.Valid {
			st.StartedAt = stStartedAt.Time
		}
		if stEndedAt.Valid {
			st.EndedAt = stEndedAt.Time
		}
		steps = append(steps, &st)
	}
	if err := stepRows.Err(); err != nil {
		return nil, fmt.Errorf("iterate steps for run %s: %w", jobID, err)
	}

	// Compute log stats for each step
	for _, st := range steps {
		var lineCount int
		var byteCount int64
		_ = s.db.QueryRowContext(ctx, `
			SELECT COUNT(*), COALESCE(SUM(LENGTH(log_line) + 1), 0)
			FROM step_logs
			WHERE run_id = ? AND step_id = ?
		`, jobID, st.ID).Scan(&lineCount, &byteCount)
		st.LogLines = lineCount
		st.LogBytes = byteCount
	}

	meta.Steps = steps
	return &meta, nil
}

// ListRuns returns run summaries sorted descending by started_at.
// If limit <= 0, all recorded runs are returned.
func (s *SQLiteStore) ListRuns(limit int) ([]*monitor.RunSummary, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	ctx := context.Background()
	query := `
		SELECT id, repo_alias, repo_url, name, workflow, owner, repo, status,
		       started_at, ended_at, duration_sec, driver, peak_cpu_percent,
		       peak_rss_bytes, step_count
		FROM runs
		ORDER BY started_at DESC
	`
	if limit > 0 {
		query += fmt.Sprintf(" LIMIT %d", limit)
	}

	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("list runs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	runs := make([]*monitor.RunSummary, 0)
	for rows.Next() {
		var (
			summary monitor.RunSummary
			endedAt sql.NullTime
		)
		if err := rows.Scan(
			&summary.ID,
			&summary.RepoAlias,
			&summary.RepoURL,
			&summary.Name,
			&summary.Workflow,
			&summary.Owner,
			&summary.Repo,
			&summary.Status,
			&summary.StartedAt,
			&endedAt,
			&summary.DurationSec,
			&summary.Driver,
			&summary.PeakCPUPercent,
			&summary.PeakRSSBytes,
			&summary.StepCount,
		); err != nil {
			return nil, fmt.Errorf("scan run summary: %w", err)
		}
		if endedAt.Valid {
			summary.EndedAt = endedAt.Time
		}
		runs = append(runs, &summary)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate run summaries: %w", err)
	}

	return runs, nil
}

// AppendStepLogs appends a batch of log lines atomically within a single transaction.
func (s *SQLiteStore) AppendStepLogs(jobID, stepID string, lines []string) error {
	if len(lines) == 0 {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	ctx := context.Background()

	// Ensure step exists so foreign key doesn't fail if log is appended before step metadata
	_, _ = s.db.ExecContext(ctx, `
		INSERT INTO steps (id, run_id, step_index, name, status)
		VALUES (?, ?, 0, ?, ?)
		ON CONFLICT(run_id, id) DO NOTHING
	`, stepID, jobID, stepID, monitor.StatusRunning)

	var maxLine int
	err := s.db.QueryRowContext(ctx, "SELECT COALESCE(MAX(line_num), 0) FROM step_logs WHERE run_id = ? AND step_id = ?", jobID, stepID).Scan(&maxLine)
	if err != nil {
		return fmt.Errorf("query max line num: %w", err)
	}
	startLine := maxLine + 1

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin log batch tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	stmt, err := tx.PrepareContext(ctx, "INSERT OR IGNORE INTO step_logs (run_id, step_id, line_num, log_line) VALUES (?, ?, ?, ?)")
	if err != nil {
		return fmt.Errorf("prepare log insert: %w", err)
	}
	defer func() { _ = stmt.Close() }()

	for i, line := range lines {
		if _, err := stmt.ExecContext(ctx, jobID, stepID, startLine+i, line); err != nil {
			return fmt.Errorf("exec log insert line %d: %w", startLine+i, err)
		}
	}

	return tx.Commit()
}

// GetStepLogs returns all lines recorded for the given step in ascending order.
func (s *SQLiteStore) GetStepLogs(jobID, stepID string) ([]string, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	ctx := context.Background()
	rows, err := s.db.QueryContext(ctx, "SELECT log_line FROM step_logs WHERE run_id = ? AND step_id = ? ORDER BY line_num ASC", jobID, stepID)
	if err != nil {
		return nil, fmt.Errorf("query step logs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	logs := make([]string, 0)
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return nil, fmt.Errorf("scan step log: %w", err)
		}
		logs = append(logs, line)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate step logs: %w", err)
	}

	return logs, nil
}

// AppendStepLog appends a single log line (satisfies LogStore interface).
func (s *SQLiteStore) AppendStepLog(jobID, stepID, line string) error {
	return s.AppendStepLogs(jobID, stepID, []string{line})
}

// GetStepLogReader returns a readable stream for the step logs (satisfies LogStore interface).
func (s *SQLiteStore) GetStepLogReader(jobID, stepID string) (io.ReadCloser, error) {
	lines, err := s.GetStepLogs(jobID, stepID)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	for _, l := range lines {
		buf.WriteString(l)
		buf.WriteByte('\n')
	}
	return io.NopCloser(&buf), nil
}

// StartRun initializes a run from job and limits (satisfies HistoryStore interface).
func (s *SQLiteStore) StartRun(job *monitor.JobInfo, limits *config.LimitsConfig, driver string) error {
	if job == nil || job.ID == "" {
		return errors.New("invalid job or empty job ID")
	}

	status := job.Status
	if status == "" {
		status = monitor.StatusRunning
	}

	startedAt := job.StartedAt
	if startedAt.IsZero() {
		startedAt = time.Now().UTC()
	}

	steps := make([]*monitor.StepMetadata, 0, len(job.Steps))
	for i, st := range job.Steps {
		if st == nil {
			continue
		}
		num := st.Number
		if num == 0 {
			num = i + 1
		}
		steps = append(steps, &monitor.StepMetadata{
			ID:          st.ID,
			Number:      num,
			Name:        st.Name,
			Status:      st.Status,
			StartedAt:   st.StartedAt,
			EndedAt:     st.EndedAt,
			DurationSec: 0,
		})
	}

	meta := &monitor.RunMetadata{
		ID:          job.ID,
		Name:        job.Name,
		Workflow:    job.Workflow,
		Repo:        job.Repo,
		RepoAlias:   job.RepoAlias,
		RepoURL:     job.RepoURL,
		Owner:       job.Owner,
		Status:      status,
		StartedAt:   startedAt,
		EndedAt:     job.EndedAt,
		Driver:      driver,
		Limits:      limits,
		Steps:       steps,
	}

	return s.RecordRun(meta)
}

// FinishStep finalizes an individual step record (satisfies HistoryStore interface).
func (s *SQLiteStore) FinishStep(jobID, stepID, conclusion string, exitCode int, finishedAt time.Time, startedAt ...time.Time) error {
	if jobID == "" || stepID == "" {
		return errors.New("empty jobID or stepID")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	ctx := context.Background()
	var sAt time.Time
	if len(startedAt) > 0 && !startedAt[0].IsZero() {
		sAt = startedAt[0]
	} else {
		var nullSAt sql.NullTime
		_ = s.db.QueryRowContext(ctx, "SELECT started_at FROM steps WHERE run_id = ? AND id = ?", jobID, stepID).Scan(&nullSAt)
		if nullSAt.Valid {
			sAt = nullSAt.Time
		}
	}

	var dur int64
	if !sAt.IsZero() && !finishedAt.IsZero() {
		dur = int64(finishedAt.Sub(sAt).Seconds())
		if dur < 0 {
			dur = 0
		}
	}

	res, err := s.db.ExecContext(ctx, `
		UPDATE steps
		SET status = ?, ended_at = ?, duration_sec = ?, exit_code = ?
		WHERE run_id = ? AND id = ?
	`, conclusion, finishedAt, dur, exitCode, jobID, stepID)
	if err != nil {
		return fmt.Errorf("finish step %s/%s: %w", jobID, stepID, err)
	}

	rows, _ := res.RowsAffected()
	if rows == 0 {
		_, err = s.db.ExecContext(ctx, `
			INSERT INTO steps (id, run_id, step_index, name, status, started_at, ended_at, duration_sec, exit_code)
			VALUES (?, ?, 0, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(run_id, id) DO UPDATE SET
				status = excluded.status,
				ended_at = excluded.ended_at,
				duration_sec = excluded.duration_sec,
				exit_code = excluded.exit_code
		`, stepID, jobID, stepID, conclusion, sAt, finishedAt, dur, exitCode)
		if err != nil {
			return fmt.Errorf("upsert finished step %s/%s: %w", jobID, stepID, err)
		}
	}
	return nil
}

// FinishRun finalizes a run record with terminal status and metrics (satisfies HistoryStore interface).
func (s *SQLiteStore) FinishRun(jobID string, status monitor.StepStatus, metrics *monitor.RunMetricsSummary) error {
	if jobID == "" {
		return errors.New("empty jobID")
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	ctx := context.Background()
	now := time.Now().UTC()

	var sAt time.Time
	var nullSAt sql.NullTime
	_ = s.db.QueryRowContext(ctx, "SELECT started_at FROM runs WHERE id = ?", jobID).Scan(&nullSAt)
	if nullSAt.Valid {
		sAt = nullSAt.Time
	}

	var dur int64
	if !sAt.IsZero() {
		dur = int64(now.Sub(sAt).Seconds())
		if dur < 0 {
			dur = 0
		}
	}

	var peakCPU float64
	var peakRSS, netRx, netTx, diskWrite uint64
	if metrics != nil {
		peakCPU = metrics.PeakCPUPercent
		peakRSS = metrics.PeakRSSBytes
		netRx = metrics.TotalNetRxBytes
		netTx = metrics.TotalNetTxBytes
		diskWrite = metrics.TotalDiskWrittenBytes
	}

	_, err := s.db.ExecContext(ctx, `
		UPDATE runs
		SET status = ?, ended_at = ?, duration_sec = ?, peak_cpu_percent = ?, peak_rss_bytes = ?,
		    total_net_rx_bytes = ?, total_net_tx_bytes = ?, total_disk_write_bytes = ?
		WHERE id = ?
	`, status, now, dur, peakCPU, peakRSS, netRx, netTx, diskWrite, jobID)
	if err != nil {
		return fmt.Errorf("finish run %s: %w", jobID, err)
	}

	if status == monitor.StatusSuccess {
		_, _ = s.db.ExecContext(ctx, `
			UPDATE steps
			SET status = ?
			WHERE run_id = ? AND status IN (?, ?, '')
		`, monitor.StatusSuccess, jobID, monitor.StatusRunning, monitor.StatusQueued)
	}

	return nil
}

// RecoverInterruptedRuns recovers runs interrupted mid-flight (satisfies Pruner interface).
func (s *SQLiteStore) RecoverInterruptedRuns() (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	ctx := context.Background()
	now := time.Now().UTC()

	rows, err := s.db.QueryContext(ctx, "SELECT id, started_at FROM runs WHERE status IN (?, ?)", monitor.StatusRunning, monitor.StatusQueued)
	if err != nil {
		return 0, fmt.Errorf("query interrupted runs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	type runItem struct {
		id        string
		startedAt time.Time
	}
	var interrupted []runItem
	for rows.Next() {
		var item runItem
		var sAt sql.NullTime
		if err := rows.Scan(&item.id, &sAt); err != nil {
			return 0, fmt.Errorf("scan interrupted run: %w", err)
		}
		if sAt.Valid {
			item.startedAt = sAt.Time
		}
		interrupted = append(interrupted, item)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("iterate interrupted runs: %w", err)
	}

	for _, item := range interrupted {
		var dur int64
		if !item.startedAt.IsZero() {
			dur = int64(now.Sub(item.startedAt).Seconds())
			if dur < 0 {
				dur = 0
			}
		}

		if _, err := s.db.ExecContext(ctx, `
			UPDATE runs
			SET status = ?, ended_at = ?, duration_sec = ?
			WHERE id = ?
		`, StatusInterrupted, now, dur, item.id); err != nil {
			return 0, fmt.Errorf("update interrupted run %s: %w", item.id, err)
		}

		stepRows, err := s.db.QueryContext(ctx, `
			SELECT id, started_at
			FROM steps
			WHERE run_id = ? AND (status IN (?, ?) OR status = '')
		`, item.id, monitor.StatusRunning, monitor.StatusQueued)
		if err == nil {
			type stepItem struct {
				id        string
				startedAt time.Time
			}
			var stepsToUpdate []stepItem
			for stepRows.Next() {
				var si stepItem
				var sNull sql.NullTime
				if err := stepRows.Scan(&si.id, &sNull); err == nil {
					if sNull.Valid {
						si.startedAt = sNull.Time
					}
					stepsToUpdate = append(stepsToUpdate, si)
				}
			}
			_ = stepRows.Close()

			for _, si := range stepsToUpdate {
				var sDur int64
				if !si.startedAt.IsZero() {
					sDur = int64(now.Sub(si.startedAt).Seconds())
					if sDur < 0 {
						sDur = 0
					}
				}
				_, _ = s.db.ExecContext(ctx, `
					UPDATE steps
					SET status = ?, ended_at = ?, duration_sec = ?
					WHERE run_id = ? AND id = ?
				`, StatusInterrupted, now, sDur, item.id, si.id)
			}
		}
	}

	return len(interrupted), nil
}

// Prune removes oldest completed runs keeping only maxRuns (satisfies Pruner interface).
func (s *SQLiteStore) Prune(maxRuns int, _ ...int64) (int, error) {
	if maxRuns <= 0 {
		return 0, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	ctx := context.Background()
	res, err := s.db.ExecContext(ctx, `
		DELETE FROM runs
		WHERE id IN (
			SELECT id FROM runs
			ORDER BY started_at DESC
			LIMIT -1 OFFSET ?
		)
	`, maxRuns)
	if err != nil {
		return 0, fmt.Errorf("prune runs: %w", err)
	}

	rows, err := res.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("prune rows affected: %w", err)
	}
	return int(rows), nil
}
