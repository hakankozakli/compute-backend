package builder

import (
	"database/sql"
	"fmt"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
)

// PostgresStore persists builds and logs to Postgres.
type PostgresStore struct {
	db *sql.DB
}

func NewPostgresStore(conn string) (*PostgresStore, error) {
	db, err := sql.Open("pgx", conn)
	if err != nil {
		return nil, fmt.Errorf("open postgres connection: %w", err)
	}
	db.SetMaxIdleConns(5)
	db.SetMaxOpenConns(10)
	db.SetConnMaxLifetime(time.Hour)

	s := &PostgresStore{db: db}
	if err := s.ensureSchema(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *PostgresStore) ensureSchema() error {
	schema := `
CREATE TABLE IF NOT EXISTS runner_builds (
    id TEXT PRIMARY KEY,
    template TEXT NOT NULL,
    runner_name TEXT NOT NULL,
    model_id TEXT NOT NULL,
    version TEXT NOT NULL,
    repository TEXT NOT NULL,
    runner_image TEXT NOT NULL,
    weights_uri TEXT,
    status TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    finished_at TIMESTAMPTZ,
    error TEXT
);
CREATE TABLE IF NOT EXISTS runner_build_logs (
    id BIGSERIAL PRIMARY KEY,
    build_id TEXT NOT NULL REFERENCES runner_builds(id) ON DELETE CASCADE,
    line TEXT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
`
	_, err := s.db.Exec(schema)
	return err
}

func (s *PostgresStore) Close() error {
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

func (s *PostgresStore) Create(build Build) error {
	query := `INSERT INTO runner_builds (id, template, runner_name, model_id, version, repository, weights_uri, status, created_at, updated_at)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
ON CONFLICT (id) DO UPDATE SET
    template = EXCLUDED.template,
    runner_name = EXCLUDED.runner_name,
    model_id = EXCLUDED.model_id,
    version = EXCLUDED.version,
    repository = EXCLUDED.repository,
    runner_image = EXCLUDED.runner_image,
    weights_uri = EXCLUDED.weights_uri,
    status = EXCLUDED.status,
    updated_at = EXCLUDED.updated_at`
	_, err := s.db.Exec(query,
		build.ID,
		build.Template,
		build.RunnerName,
		build.ModelID,
		build.Version,
		build.Repository,
		build.RunnerImage,
		build.WeightsURI,
		build.Status,
		build.CreatedAt,
		build.UpdatedAt,
	)
	return err
}

func (s *PostgresStore) UpdateStatus(id string, status Status, finishedAt *time.Time, errMsg string) error {
	query := `UPDATE runner_builds SET status=$1, updated_at=$2, finished_at=$3, error=$4 WHERE id=$5`
	_, err := s.db.Exec(query, status, time.Now().UTC(), finishedAt, errMsg, id)
	return err
}

func (s *PostgresStore) AppendLog(id string, line string) error {
	_, err := s.db.Exec(`INSERT INTO runner_build_logs (build_id, line) VALUES ($1,$2)`, id, line)
	return err
}

func (s *PostgresStore) List() ([]Build, error) {
	rows, err := s.db.Query(`SELECT id, template, runner_name, model_id, version, repository, runner_image, weights_uri, status, created_at, updated_at, finished_at, error FROM runner_builds ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var builds []Build
	for rows.Next() {
		var b Build
		var finishedAt sql.NullTime
		var weights sql.NullString
		var errMsg sql.NullString
		if err := rows.Scan(&b.ID, &b.Template, &b.RunnerName, &b.ModelID, &b.Version, &b.Repository, &b.RunnerImage, &weights, &b.Status, &b.CreatedAt, &b.UpdatedAt, &finishedAt, &errMsg); err != nil {
			return nil, err
		}
		if weights.Valid {
			b.WeightsURI = weights.String
		}
		if finishedAt.Valid {
			b.FinishedAt = finishedAt.Time
		}
		if errMsg.Valid {
			b.Error = errMsg.String
		}
		builds = append(builds, b)
	}
	return builds, rows.Err()
}

func (s *PostgresStore) Get(id string) (Build, error) {
	var b Build
	var finishedAt sql.NullTime
	var weights sql.NullString
	var errMsg sql.NullString
	query := `SELECT id, template, runner_name, model_id, version, repository, runner_image, weights_uri, status, created_at, updated_at, finished_at, error FROM runner_builds WHERE id=$1`
	err := s.db.QueryRow(query, id).Scan(&b.ID, &b.Template, &b.RunnerName, &b.ModelID, &b.Version, &b.Repository, &b.RunnerImage, &weights, &b.Status, &b.CreatedAt, &b.UpdatedAt, &finishedAt, &errMsg)
	if err != nil {
		return Build{}, err
	}
	if weights.Valid {
		b.WeightsURI = weights.String
	}
	if finishedAt.Valid {
		b.FinishedAt = finishedAt.Time
	}
	if errMsg.Valid {
		b.Error = errMsg.String
	}
	return b, nil
}

func (s *PostgresStore) ListLogs(id string, limit int) ([]string, error) {
	rows, err := s.db.Query(`SELECT line FROM runner_build_logs WHERE build_id=$1 ORDER BY id ASC LIMIT $2`, id, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var lines []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return nil, err
		}
		lines = append(lines, line)
	}
	return lines, rows.Err()
}
