package queue

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"

	"maxwell/internal/config"
	"maxwell/internal/model"
)

type Store struct {
	db       *sql.DB
	driver   string
	rebinder rebindFunc
}

type rebindFunc func(string) string

func Open(state config.StateStoreConfig) (*Store, error) {
	driver := strings.ToLower(strings.TrimSpace(state.Driver))
	if driver == "" {
		driver = "sqlite"
	}

	dsn := strings.TrimSpace(state.DSN)
	if dsn == "" {
		return nil, fmt.Errorf("state_store.dsn is required")
	}

	sqlDriver := mapDriver(driver)
	db, err := sql.Open(sqlDriver, dsn)
	if err != nil {
		return nil, err
	}
	maxConns := state.MaxOpenConns
	if maxConns <= 0 {
		maxConns = 1
	}
	db.SetMaxOpenConns(maxConns)

	s := &Store{db: db, driver: driver, rebinder: selectRebinder(driver)}
	if err := s.migrate(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func mapDriver(driver string) string {
	switch driver {
	case "postgres":
		return "pgx"
	case "mysql":
		return "mysql"
	default:
		return "sqlite"
	}
}

func selectRebinder(driver string) rebindFunc {
	switch driver {
	case "postgres":
		return rebindPostgres
	default:
		return func(q string) string { return q }
	}
}

func (s *Store) q(query string) string { return s.rebinder(query) }

func rebindPostgres(query string) string {
	if !strings.Contains(query, "?") {
		return query
	}
	out := strings.Builder{}
	index := 1
	for i := 0; i < len(query); i++ {
		if query[i] == '?' {
			out.WriteString(fmt.Sprintf("$%d", index))
			index++
			continue
		}
		out.WriteByte(query[i])
	}
	return out.String()
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate() error {
	stmts := []string{}
	switch s.driver {
	case "postgres":
		stmts = postgresMigrations()
	case "mysql":
		stmts = mysqlMigrations()
	default:
		stmts = sqliteMigrations()
	}

	for _, stmt := range stmts {
		if err := s.execMigration(stmt); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) execMigration(stmt string) error {
	if _, err := s.db.Exec(stmt); err != nil {
		if isIgnorableMigrationErr(err) {
			return nil
		}
		return err
	}
	return nil
}

func isIgnorableMigrationErr(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "duplicate column name") {
		return true
	}
	if strings.Contains(msg, "already exists") && strings.Contains(msg, "column") {
		return true
	}
	return false
}

func sqliteMigrations() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS downloads (
			hash TEXT PRIMARY KEY,
			source_path TEXT NOT NULL,
			status TEXT NOT NULL,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		);`,
		`CREATE TABLE IF NOT EXISTS conversion_jobs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			torrent_hash TEXT NOT NULL,
			input_path TEXT NOT NULL,
			output_path TEXT NOT NULL,
			preset TEXT NOT NULL,
			status TEXT NOT NULL,
			attempts INTEGER NOT NULL DEFAULT 0,
			error TEXT NOT NULL DEFAULT '',
			next_attempt_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(input_path, preset)
		);`,
		`ALTER TABLE conversion_jobs ADD COLUMN next_attempt_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP;`,
		`CREATE TABLE IF NOT EXISTS upload_jobs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			file_path TEXT NOT NULL,
			object_key TEXT NOT NULL,
			status TEXT NOT NULL,
			attempts INTEGER NOT NULL DEFAULT 0,
			error TEXT NOT NULL DEFAULT '',
			final_url TEXT NOT NULL DEFAULT '',
			next_attempt_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE(file_path, object_key)
		);`,
		`ALTER TABLE upload_jobs ADD COLUMN next_attempt_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP;`,
		`CREATE TABLE IF NOT EXISTS links (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			file_path TEXT NOT NULL,
			object_key TEXT NOT NULL,
			final_url TEXT NOT NULL UNIQUE,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		);`,
		`CREATE TABLE IF NOT EXISTS events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			level TEXT NOT NULL,
			type TEXT NOT NULL,
			message TEXT NOT NULL,
			created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
		);`,
		`CREATE INDEX IF NOT EXISTS idx_conv_status_next ON conversion_jobs(status, next_attempt_at);`,
		`CREATE INDEX IF NOT EXISTS idx_upload_status_next ON upload_jobs(status, next_attempt_at);`,
	}
}

func postgresMigrations() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS downloads (
			hash TEXT PRIMARY KEY,
			source_path TEXT NOT NULL,
			status TEXT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);`,
		`CREATE TABLE IF NOT EXISTS conversion_jobs (
			id BIGSERIAL PRIMARY KEY,
			torrent_hash TEXT NOT NULL,
			input_path TEXT NOT NULL,
			output_path TEXT NOT NULL,
			preset TEXT NOT NULL,
			status TEXT NOT NULL,
			attempts INTEGER NOT NULL DEFAULT 0,
			error TEXT NOT NULL DEFAULT '',
			next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			UNIQUE(input_path, preset)
		);`,
		`ALTER TABLE conversion_jobs ADD COLUMN IF NOT EXISTS next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW();`,
		`CREATE TABLE IF NOT EXISTS upload_jobs (
			id BIGSERIAL PRIMARY KEY,
			file_path TEXT NOT NULL,
			object_key TEXT NOT NULL,
			status TEXT NOT NULL,
			attempts INTEGER NOT NULL DEFAULT 0,
			error TEXT NOT NULL DEFAULT '',
			final_url TEXT NOT NULL DEFAULT '',
			next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
			UNIQUE(file_path, object_key)
		);`,
		`ALTER TABLE upload_jobs ADD COLUMN IF NOT EXISTS next_attempt_at TIMESTAMPTZ NOT NULL DEFAULT NOW();`,
		`CREATE TABLE IF NOT EXISTS links (
			id BIGSERIAL PRIMARY KEY,
			file_path TEXT NOT NULL,
			object_key TEXT NOT NULL,
			final_url TEXT NOT NULL UNIQUE,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);`,
		`CREATE TABLE IF NOT EXISTS events (
			id BIGSERIAL PRIMARY KEY,
			level TEXT NOT NULL,
			type TEXT NOT NULL,
			message TEXT NOT NULL,
			created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
		);`,
		`CREATE INDEX IF NOT EXISTS idx_conv_status_next ON conversion_jobs(status, next_attempt_at);`,
		`CREATE INDEX IF NOT EXISTS idx_upload_status_next ON upload_jobs(status, next_attempt_at);`,
	}
}

func mysqlMigrations() []string {
	return []string{
		`CREATE TABLE IF NOT EXISTS downloads (
			hash VARCHAR(255) PRIMARY KEY,
			source_path TEXT NOT NULL,
			status VARCHAR(32) NOT NULL,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		);`,
		`CREATE TABLE IF NOT EXISTS conversion_jobs (
			id BIGINT AUTO_INCREMENT PRIMARY KEY,
			torrent_hash VARCHAR(255) NOT NULL,
			input_path TEXT NOT NULL,
			output_path TEXT NOT NULL,
			preset VARCHAR(128) NOT NULL,
			status VARCHAR(32) NOT NULL,
			attempts INT NOT NULL DEFAULT 0,
			error TEXT NOT NULL,
			next_attempt_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
			UNIQUE KEY uniq_conv_input_preset (input_path(255), preset)
		);`,
		`ALTER TABLE conversion_jobs ADD COLUMN next_attempt_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP;`,
		`CREATE TABLE IF NOT EXISTS upload_jobs (
			id BIGINT AUTO_INCREMENT PRIMARY KEY,
			file_path TEXT NOT NULL,
			object_key TEXT NOT NULL,
			status VARCHAR(32) NOT NULL,
			attempts INT NOT NULL DEFAULT 0,
			error TEXT NOT NULL,
			final_url TEXT NOT NULL,
			next_attempt_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
			UNIQUE KEY uniq_upload_file_key (file_path(255), object_key(255))
		);`,
		`ALTER TABLE upload_jobs ADD COLUMN next_attempt_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP;`,
		`CREATE TABLE IF NOT EXISTS links (
			id BIGINT AUTO_INCREMENT PRIMARY KEY,
			file_path TEXT NOT NULL,
			object_key TEXT NOT NULL,
			final_url TEXT NOT NULL,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			UNIQUE KEY uniq_final_url (final_url(255))
		);`,
		`CREATE TABLE IF NOT EXISTS events (
			id BIGINT AUTO_INCREMENT PRIMARY KEY,
			level VARCHAR(32) NOT NULL,
			type VARCHAR(64) NOT NULL,
			message TEXT NOT NULL,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		);`,
		`CREATE INDEX idx_conv_status_next ON conversion_jobs(status, next_attempt_at);`,
		`CREATE INDEX idx_upload_status_next ON upload_jobs(status, next_attempt_at);`,
	}
}

func (s *Store) AddEvent(ctx context.Context, level, eventType, message string) error {
	_, err := s.db.ExecContext(ctx,
		s.q(`INSERT INTO events(level, type, message) VALUES (?, ?, ?)`),
		level, eventType, message,
	)
	return err
}

func (s *Store) ListEvents(ctx context.Context, limit int) ([]model.Event, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, s.q(`SELECT id, level, type, message, created_at FROM events ORDER BY id DESC LIMIT ?`), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []model.Event
	for rows.Next() {
		var e model.Event
		if err := rows.Scan(&e.ID, &e.Level, &e.Type, &e.Message, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func (s *Store) RecordDownload(ctx context.Context, hash, sourcePath, status string) error {
	switch s.driver {
	case "postgres":
		_, err := s.db.ExecContext(ctx,
			s.q(`INSERT INTO downloads(hash, source_path, status) VALUES (?, ?, ?)
			ON CONFLICT(hash) DO UPDATE SET source_path=excluded.source_path, status=excluded.status`),
			hash, sourcePath, status,
		)
		return err
	case "mysql":
		_, err := s.db.ExecContext(ctx,
			s.q(`INSERT INTO downloads(hash, source_path, status) VALUES (?, ?, ?)
			ON DUPLICATE KEY UPDATE source_path=VALUES(source_path), status=VALUES(status)`),
			hash, sourcePath, status,
		)
		return err
	default:
		_, err := s.db.ExecContext(ctx,
			s.q(`INSERT INTO downloads(hash, source_path, status) VALUES (?, ?, ?)
			 ON CONFLICT(hash) DO UPDATE SET source_path=excluded.source_path, status=excluded.status`),
			hash, sourcePath, status,
		)
		return err
	}
}

func (s *Store) EnqueueConversion(ctx context.Context, torrentHash, inputPath, outputPath, preset string) (bool, error) {
	res, err := s.insertIgnoreConversion(ctx, torrentHash, inputPath, outputPath, preset)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

func (s *Store) insertIgnoreConversion(ctx context.Context, torrentHash, inputPath, outputPath, preset string) (sql.Result, error) {
	now := time.Now().UTC()
	switch s.driver {
	case "postgres":
		return s.db.ExecContext(ctx,
			s.q(`INSERT INTO conversion_jobs(torrent_hash, input_path, output_path, preset, status, next_attempt_at)
			VALUES (?, ?, ?, ?, ?, ?) ON CONFLICT(input_path, preset) DO NOTHING`),
			torrentHash, inputPath, outputPath, preset, string(model.JobStatusQueued), now,
		)
	case "mysql":
		return s.db.ExecContext(ctx,
			s.q(`INSERT IGNORE INTO conversion_jobs(torrent_hash, input_path, output_path, preset, status, next_attempt_at)
			VALUES (?, ?, ?, ?, ?, ?)`),
			torrentHash, inputPath, outputPath, preset, string(model.JobStatusQueued), now,
		)
	default:
		return s.db.ExecContext(ctx,
			s.q(`INSERT OR IGNORE INTO conversion_jobs(torrent_hash, input_path, output_path, preset, status, next_attempt_at)
			VALUES (?, ?, ?, ?, ?, ?)`),
			torrentHash, inputPath, outputPath, preset, string(model.JobStatusQueued), now,
		)
	}
}

func (s *Store) ListConversionJobs(ctx context.Context) ([]model.ConversionJob, error) {
	rows, err := s.db.QueryContext(ctx, s.q(`
		SELECT id, torrent_hash, input_path, output_path, preset, status, attempts, error, created_at, updated_at
		FROM conversion_jobs ORDER BY id ASC`))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.ConversionJob
	for rows.Next() {
		var j model.ConversionJob
		if err := rows.Scan(&j.ID, &j.TorrentID, &j.InputPath, &j.OutputPath, &j.Preset, &j.Status, &j.Attempts, &j.Error, &j.CreatedAt, &j.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

func (s *Store) NextQueuedConversion(ctx context.Context) (model.ConversionJob, error) {
	row := s.db.QueryRowContext(ctx, s.q(`
		SELECT id, torrent_hash, input_path, output_path, preset, status, attempts, error, created_at, updated_at
		FROM conversion_jobs WHERE status=? AND next_attempt_at <= ? ORDER BY id ASC LIMIT 1`), string(model.JobStatusQueued), time.Now().UTC())
	var j model.ConversionJob
	if err := row.Scan(&j.ID, &j.TorrentID, &j.InputPath, &j.OutputPath, &j.Preset, &j.Status, &j.Attempts, &j.Error, &j.CreatedAt, &j.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.ConversionJob{}, sql.ErrNoRows
		}
		return model.ConversionJob{}, err
	}
	return j, nil
}

func (s *Store) MarkConversionRunning(ctx context.Context, id int64, attempts int) error {
	_, err := s.db.ExecContext(ctx,
		s.q(`UPDATE conversion_jobs SET status=?, attempts=?, updated_at=? WHERE id=?`),
		string(model.JobStatusRunning), attempts, time.Now().UTC(), id,
	)
	return err
}

func (s *Store) MarkConversionDone(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx,
		s.q(`UPDATE conversion_jobs SET status=?, updated_at=? WHERE id=?`),
		string(model.JobStatusDone), time.Now().UTC(), id,
	)
	return err
}

func (s *Store) MarkConversionFailed(ctx context.Context, id int64, errMessage string, retryAt time.Time, requeue bool) error {
	status := string(model.JobStatusFailed)
	if requeue {
		status = string(model.JobStatusQueued)
	}
	_, err := s.db.ExecContext(ctx,
		s.q(`UPDATE conversion_jobs SET status=?, error=?, next_attempt_at=?, updated_at=? WHERE id=?`),
		status, errMessage, retryAt.UTC(), time.Now().UTC(), id,
	)
	return err
}

func (s *Store) PauseConversionJob(ctx context.Context, id int64) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		s.q(`UPDATE conversion_jobs SET status=?, updated_at=? WHERE id=? AND status=?`),
		string(model.JobStatusPaused), time.Now().UTC(), id, string(model.JobStatusQueued),
	)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

func (s *Store) ResumeConversionJob(ctx context.Context, id int64) (bool, error) {
	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx,
		s.q(`UPDATE conversion_jobs SET status=?, error=?, next_attempt_at=?, updated_at=? WHERE id=? AND status=?`),
		string(model.JobStatusQueued), "", now, now, id, string(model.JobStatusPaused),
	)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

func (s *Store) EnqueueUpload(ctx context.Context, filePath, objectKey string) (bool, error) {
	res, err := s.insertIgnoreUpload(ctx, filePath, objectKey)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

func (s *Store) insertIgnoreUpload(ctx context.Context, filePath, objectKey string) (sql.Result, error) {
	now := time.Now().UTC()
	switch s.driver {
	case "postgres":
		return s.db.ExecContext(ctx,
			s.q(`INSERT INTO upload_jobs(file_path, object_key, status, next_attempt_at)
			VALUES (?, ?, ?, ?) ON CONFLICT(file_path, object_key) DO NOTHING`),
			filePath, objectKey, string(model.JobStatusQueued), now,
		)
	case "mysql":
		return s.db.ExecContext(ctx,
			s.q(`INSERT IGNORE INTO upload_jobs(file_path, object_key, status, next_attempt_at)
			VALUES (?, ?, ?, ?)`),
			filePath, objectKey, string(model.JobStatusQueued), now,
		)
	default:
		return s.db.ExecContext(ctx,
			s.q(`INSERT OR IGNORE INTO upload_jobs(file_path, object_key, status, next_attempt_at)
			VALUES (?, ?, ?, ?)`),
			filePath, objectKey, string(model.JobStatusQueued), now,
		)
	}
}

func (s *Store) ListUploadJobs(ctx context.Context) ([]model.UploadJob, error) {
	rows, err := s.db.QueryContext(ctx, s.q(`
		SELECT id, file_path, object_key, status, attempts, final_url, error, created_at, updated_at
		FROM upload_jobs ORDER BY id ASC`))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.UploadJob
	for rows.Next() {
		var j model.UploadJob
		if err := rows.Scan(&j.ID, &j.FilePath, &j.ObjectKey, &j.Status, &j.Attempts, &j.FinalURL, &j.Error, &j.CreatedAt, &j.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, j)
	}
	return out, rows.Err()
}

func (s *Store) NextQueuedUpload(ctx context.Context) (model.UploadJob, error) {
	row := s.db.QueryRowContext(ctx, s.q(`
		SELECT id, file_path, object_key, status, attempts, final_url, error, created_at, updated_at
		FROM upload_jobs WHERE status=? AND next_attempt_at <= ? ORDER BY id ASC LIMIT 1`), string(model.JobStatusQueued), time.Now().UTC())
	var j model.UploadJob
	if err := row.Scan(&j.ID, &j.FilePath, &j.ObjectKey, &j.Status, &j.Attempts, &j.FinalURL, &j.Error, &j.CreatedAt, &j.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return model.UploadJob{}, sql.ErrNoRows
		}
		return model.UploadJob{}, err
	}
	return j, nil
}

func (s *Store) MarkUploadRunning(ctx context.Context, id int64, attempts int) error {
	_, err := s.db.ExecContext(ctx,
		s.q(`UPDATE upload_jobs SET status=?, attempts=?, updated_at=? WHERE id=?`),
		string(model.JobStatusRunning), attempts, time.Now().UTC(), id,
	)
	return err
}

func (s *Store) MarkUploadDone(ctx context.Context, id int64, finalURL string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	if _, err = tx.ExecContext(ctx,
		s.q(`UPDATE upload_jobs SET status=?, final_url=?, updated_at=? WHERE id=?`),
		string(model.JobStatusDone), finalURL, time.Now().UTC(), id,
	); err != nil {
		return err
	}

	var filePath, objectKey string
	if err = tx.QueryRowContext(ctx, s.q(`SELECT file_path, object_key FROM upload_jobs WHERE id=?`), id).Scan(&filePath, &objectKey); err != nil {
		return err
	}

	switch s.driver {
	case "mysql":
		if _, err = tx.ExecContext(ctx,
			s.q(`INSERT IGNORE INTO links(file_path, object_key, final_url) VALUES (?, ?, ?)`),
			filePath, objectKey, finalURL,
		); err != nil {
			return err
		}
	default:
		if _, err = tx.ExecContext(ctx,
			s.q(`INSERT INTO links(file_path, object_key, final_url) VALUES (?, ?, ?) ON CONFLICT DO NOTHING`),
			filePath, objectKey, finalURL,
		); err != nil {
			return err
		}
	}

	if err = tx.Commit(); err != nil {
		return err
	}
	return nil
}

func (s *Store) MarkUploadFailed(ctx context.Context, id int64, errMessage string, retryAt time.Time, requeue bool) error {
	status := string(model.JobStatusFailed)
	if requeue {
		status = string(model.JobStatusQueued)
	}
	_, err := s.db.ExecContext(ctx,
		s.q(`UPDATE upload_jobs SET status=?, error=?, next_attempt_at=?, updated_at=? WHERE id=?`),
		status, errMessage, retryAt.UTC(), time.Now().UTC(), id,
	)
	return err
}

func (s *Store) PauseUploadJob(ctx context.Context, id int64) (bool, error) {
	res, err := s.db.ExecContext(ctx,
		s.q(`UPDATE upload_jobs SET status=?, updated_at=? WHERE id=? AND status=?`),
		string(model.JobStatusPaused), time.Now().UTC(), id, string(model.JobStatusQueued),
	)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

func (s *Store) ResumeUploadJob(ctx context.Context, id int64) (bool, error) {
	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx,
		s.q(`UPDATE upload_jobs SET status=?, error=?, next_attempt_at=?, updated_at=? WHERE id=? AND status=?`),
		string(model.JobStatusQueued), "", now, now, id, string(model.JobStatusPaused),
	)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

func (s *Store) ListLinks(ctx context.Context, limit int) ([]model.LinkRecord, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, s.q(`
		SELECT id, file_path, object_key, final_url, created_at FROM links ORDER BY id DESC LIMIT ?`), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []model.LinkRecord
	for rows.Next() {
		var l model.LinkRecord
		if err := rows.Scan(&l.ID, &l.FilePath, &l.ObjectKey, &l.FinalURL, &l.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

func (s *Store) Stats(ctx context.Context) (map[string]int64, error) {
	counts := map[string]int64{}
	queries := map[string]string{
		"downloads":   `SELECT COUNT(*) FROM downloads`,
		"conversion":  `SELECT COUNT(*) FROM conversion_jobs`,
		"upload":      `SELECT COUNT(*) FROM upload_jobs`,
		"links":       `SELECT COUNT(*) FROM links`,
		"events":      `SELECT COUNT(*) FROM events`,
		"conv_queued": `SELECT COUNT(*) FROM conversion_jobs WHERE status='queued'`,
		"upl_queued":  `SELECT COUNT(*) FROM upload_jobs WHERE status='queued'`,
	}
	for k, q := range queries {
		var n int64
		if err := s.db.QueryRowContext(ctx, s.q(q)).Scan(&n); err != nil {
			return nil, fmt.Errorf("stats %s: %w", k, err)
		}
		counts[k] = n
	}
	return counts, nil
}
