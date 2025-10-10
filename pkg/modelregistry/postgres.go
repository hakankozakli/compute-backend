package modelregistry

import (
	"database/sql"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	_ "github.com/jackc/pgx/v5/stdlib"
)

//go:embed migrations/*.sql
var migrationsFS embed.FS

// PostgresStore implements Store backed by PostgreSQL.
type PostgresStore struct {
	db *sql.DB
}

// NewPostgresStore initialises a Postgres-backed model registry store.
func NewPostgresStore(connString string) (*PostgresStore, error) {
	if strings.TrimSpace(connString) == "" {
		return nil, fmt.Errorf("postgres connection string is required")
	}
	db, err := sql.Open("pgx", connString)
	if err != nil {
		return nil, fmt.Errorf("open postgres connection: %w", err)
	}
	db.SetMaxIdleConns(5)
	db.SetMaxOpenConns(20)
	db.SetConnMaxIdleTime(5 * time.Minute)
	db.SetConnMaxLifetime(30 * time.Minute)

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("ping postgres: %w", err)
	}

	return &PostgresStore{db: db}, nil
}

// EnsureSchema applies embedded migrations in lexical order.
func (s *PostgresStore) EnsureSchema() error {
	entries, err := migrationsFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		if !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("begin migration tx: %w", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	for _, name := range names {
		payload, err := migrationsFS.ReadFile("migrations/" + name)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		sqlText := strings.TrimSpace(string(payload))
		if sqlText == "" {
			continue
		}
		if _, err := tx.Exec(sqlText); err != nil {
			return fmt.Errorf("apply migration %s: %w", name, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migrations: %w", err)
	}
	return nil
}

// CreateModel inserts a new model family.
func (s *PostgresStore) CreateModel(input CreateModelInput) (Model, error) {
	if strings.TrimSpace(input.ExternalID) == "" {
		return Model{}, fmt.Errorf("external_id is required")
	}
	if strings.TrimSpace(input.DisplayName) == "" {
		return Model{}, fmt.Errorf("display_name is required")
	}
	if strings.TrimSpace(input.Family) == "" {
		return Model{}, fmt.Errorf("family is required")
	}

	metadata := input.Metadata
	if metadata == nil {
		metadata = map[string]any{}
	}

	id := uuid.NewString()
	now := time.Now().UTC()
	metadataBytes, err := json.Marshal(metadata)
	if err != nil {
		return Model{}, fmt.Errorf("marshal metadata: %w", err)
	}

	const query = `
        INSERT INTO models (
            id, external_id, display_name, family, description, metadata, status, created_at, updated_at
        ) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
        RETURNING id, external_id, display_name, family, description, metadata, status, default_version_id, created_at, updated_at
    `

	row := s.db.QueryRow(query,
		id,
		strings.TrimSpace(input.ExternalID),
		strings.TrimSpace(input.DisplayName),
		strings.TrimSpace(input.Family),
		strings.TrimSpace(input.Description),
		metadataBytes,
		StatusInactive,
		now,
		now,
	)

	return scanModel(row)
}

// CreateVersion adds a new version for a model.
func (s *PostgresStore) CreateVersion(modelID string, input CreateVersionInput) (ModelVersion, error) {
	if strings.TrimSpace(modelID) == "" {
		return ModelVersion{}, fmt.Errorf("model_id is required")
	}
	if strings.TrimSpace(input.Version) == "" {
		return ModelVersion{}, fmt.Errorf("version is required")
	}
	if strings.TrimSpace(input.RunnerImage) == "" {
		return ModelVersion{}, fmt.Errorf("runner_image is required")
	}

	params := input.Parameters
	if params == nil {
		params = map[string]any{}
	}
	paramsBytes, err := json.Marshal(params)
	if err != nil {
		return ModelVersion{}, fmt.Errorf("marshal parameters: %w", err)
	}
	runnerManifest := input.RunnerManifest
	if runnerManifest == nil {
		runnerManifest = map[string]any{}
	}
	runnerManifestBytes, err := json.Marshal(runnerManifest)
	if err != nil {
		return ModelVersion{}, fmt.Errorf("marshal runner manifest: %w", err)
	}
	defaultParams := input.DefaultParameters
	if defaultParams == nil {
		defaultParams = map[string]any{}
	}
	defaultParamsBytes, err := json.Marshal(defaultParams)
	if err != nil {
		return ModelVersion{}, fmt.Errorf("marshal default parameters: %w", err)
	}

	status := input.Status
	if strings.TrimSpace(status) == "" {
		status = StatusDraft
	}

	id := uuid.NewString()
	now := time.Now().UTC()

	const query = `
        INSERT INTO model_versions (
            id, model_id, version, runner_image, runner_family, runner_manifest, default_parameters,
            weights_uri, min_gpu_mem_gb, min_vram_gb, max_batch_size,
            parameters, capabilities, latency_tier, artifact_uri, status, created_at, updated_at
        ) VALUES (
            $1, $2, $3, $4, $5, $6, $7,
            $8, $9, $10, $11,
            $12, $13, $14, $15, $16, $17, $18
        )
        RETURNING id, model_id, version, runner_image, runner_family, runner_manifest, default_parameters,
                  weights_uri, min_gpu_mem_gb, min_vram_gb, max_batch_size,
                  parameters, capabilities, latency_tier, artifact_uri, status, created_at, updated_at
    `

	row := s.db.QueryRow(query,
		id,
		modelID,
		strings.TrimSpace(input.Version),
		strings.TrimSpace(input.RunnerImage),
		nullableString(input.RunnerFamily),
		runnerManifestBytes,
		defaultParamsBytes,
		nullableString(input.WeightsURI),
		nullableInt(input.MinGPUMemGB),
		nullableInt(input.MinVRAMGB),
		nullableInt(input.MaxBatchSize),
		paramsBytes,
		pqStringArray(input.Capabilities),
		nullableString(input.LatencyTier),
		nullableString(input.ArtifactURI),
		status,
		now,
		now,
	)

	return scanVersion(row)
}

func (s *PostgresStore) UpdateVersionRunnerConfig(modelID, versionID string, input UpdateRunnerConfigInput) (ModelVersion, error) {
	if strings.TrimSpace(modelID) == "" {
		return ModelVersion{}, fmt.Errorf("model_id is required")
	}
	if strings.TrimSpace(versionID) == "" {
		return ModelVersion{}, fmt.Errorf("version_id is required")
	}

	setClauses := make([]string, 0, 6)
	args := make([]any, 0, 8)
	idx := 1

	if input.RunnerImage != nil {
		trimmed := strings.TrimSpace(*input.RunnerImage)
		if trimmed == "" {
			return ModelVersion{}, fmt.Errorf("runner_image cannot be empty")
		}
		setClauses = append(setClauses, fmt.Sprintf("runner_image = $%d", idx))
		args = append(args, trimmed)
		idx++
	}
	if input.RunnerFamily != nil {
		setClauses = append(setClauses, fmt.Sprintf("runner_family = $%d", idx))
		args = append(args, nullableString(input.RunnerFamily))
		idx++
	}
	if input.RunnerManifest != nil {
		manifestBytes, err := json.Marshal(input.RunnerManifest)
		if err != nil {
			return ModelVersion{}, fmt.Errorf("marshal runner manifest: %w", err)
		}
		setClauses = append(setClauses, fmt.Sprintf("runner_manifest = $%d", idx))
		args = append(args, manifestBytes)
		idx++
	}
	if input.DefaultParameters != nil {
		paramsBytes, err := json.Marshal(input.DefaultParameters)
		if err != nil {
			return ModelVersion{}, fmt.Errorf("marshal default parameters: %w", err)
		}
		setClauses = append(setClauses, fmt.Sprintf("default_parameters = $%d", idx))
		args = append(args, paramsBytes)
		idx++
	}

	if len(setClauses) == 0 {
		return ModelVersion{}, fmt.Errorf("no runner configuration fields provided")
	}

	setClauses = append(setClauses, fmt.Sprintf("updated_at = $%d", idx))
	args = append(args, time.Now().UTC())
	idx++

	setSQL := strings.Join(setClauses, ", ")
	query := fmt.Sprintf(`
		UPDATE model_versions
		SET %s
		WHERE model_id = $%d AND id = $%d
		RETURNING id, model_id, version, runner_image, runner_family, runner_manifest, default_parameters,
		          weights_uri, min_gpu_mem_gb, min_vram_gb, max_batch_size,
		          parameters, capabilities, latency_tier, artifact_uri, status, created_at, updated_at
	`, setSQL, idx, idx+1)

	args = append(args, modelID, versionID)

	row := s.db.QueryRow(query, args...)
	return scanVersion(row)
}

// ListModels returns models matching filters with their default version info.
func (s *PostgresStore) ListModels(opts QueryOptions) ([]Model, error) {
	const baseQuery = `
        SELECT m.id, m.external_id, m.display_name, m.family, m.description, m.metadata,
               m.status, m.default_version_id, m.created_at, m.updated_at,
               dv.id, dv.version, dv.status, dv.runner_image, dv.runner_family, dv.created_at, dv.updated_at
        FROM models m
        LEFT JOIN model_versions dv ON dv.id = m.default_version_id
    `

	var (
		clauses []string
		args    []any
		idx     = 1
	)

	if len(opts.Status) > 0 {
		placeholders := make([]string, len(opts.Status))
		for i, status := range opts.Status {
			placeholders[i] = fmt.Sprintf("$%d", idx)
			args = append(args, status)
			idx++
		}
		clauses = append(clauses, "m.status IN ("+strings.Join(placeholders, ",")+")")
	}
	if len(opts.Family) > 0 {
		placeholders := make([]string, len(opts.Family))
		for i, family := range opts.Family {
			placeholders[i] = fmt.Sprintf("$%d", idx)
			args = append(args, family)
			idx++
		}
		clauses = append(clauses, "m.family IN ("+strings.Join(placeholders, ",")+")")
	}

	query := baseQuery
	if len(clauses) > 0 {
		query += " WHERE " + strings.Join(clauses, " AND ")
	}

	query += " ORDER BY m.created_at DESC"

	if opts.Limit > 0 {
		query += fmt.Sprintf(" LIMIT $%d", idx)
		args = append(args, opts.Limit)
		idx++
	}
	if opts.Offset > 0 {
		query += fmt.Sprintf(" OFFSET $%d", idx)
		args = append(args, opts.Offset)
		idx++
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("list models: %w", err)
	}
	defer rows.Close()

	var models []Model
	for rows.Next() {
		model, version, err := scanModelWithVersion(rows)
		if err != nil {
			return nil, err
		}
		if version != nil {
			model.LatestVersion = version
		}
		models = append(models, model)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate models: %w", err)
	}

	return models, nil
}

// GetModel fetches a model and attaches default version metadata.
func (s *PostgresStore) GetModel(id string) (Model, error) {
	const query = `
        SELECT m.id, m.external_id, m.display_name, m.family, m.description, m.metadata,
               m.status, m.default_version_id, m.created_at, m.updated_at,
               dv.id, dv.version, dv.status, dv.runner_image, dv.runner_family, dv.created_at, dv.updated_at
        FROM models m
        LEFT JOIN model_versions dv ON dv.id = m.default_version_id
        WHERE m.id = $1
    `

	row := s.db.QueryRow(query, id)
	model, version, err := scanModelWithVersion(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Model{}, fmt.Errorf("model not found")
		}
		return Model{}, err
	}
	if version != nil {
		model.LatestVersion = version
	}
	return model, nil
}

// PromoteVersion sets default version for model and marks it ready.
func (s *PostgresStore) PromoteVersion(modelID, versionID string) error {
	const query = `
        UPDATE models
        SET default_version_id = $1, status = $2, updated_at = NOW()
        WHERE id = $3
          AND EXISTS (
            SELECT 1 FROM model_versions v WHERE v.id = $1 AND v.model_id = $3
          )
    `
	res, err := s.db.Exec(query, versionID, StatusReady, modelID)
	if err != nil {
		return fmt.Errorf("promote version: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return fmt.Errorf("promote version rows: %w", err)
	}
	if affected == 0 {
		return fmt.Errorf("model not found")
	}
	return nil
}

// Close releases database resources.
func (s *PostgresStore) Close() error {
	if s.db != nil {
		return s.db.Close()
	}
	return nil
}

func scanModel(scanner interface{ Scan(dest ...any) error }) (Model, error) {
	var (
		model          Model
		metadataBytes  []byte
		defaultVersion sql.NullString
	)

	err := scanner.Scan(
		&model.ID,
		&model.ExternalID,
		&model.DisplayName,
		&model.Family,
		&model.Description,
		&metadataBytes,
		&model.Status,
		&defaultVersion,
		&model.CreatedAt,
		&model.UpdatedAt,
	)
	if err != nil {
		return Model{}, fmt.Errorf("scan model: %w", err)
	}

	if len(metadataBytes) > 0 {
		if err := json.Unmarshal(metadataBytes, &model.Metadata); err != nil {
			return Model{}, fmt.Errorf("decode metadata: %w", err)
		}
	} else {
		model.Metadata = map[string]any{}
	}

	if defaultVersion.Valid {
		model.DefaultVersionID = &defaultVersion.String
	}

	return model, nil
}

func scanModelWithVersion(scanner interface{ Scan(dest ...any) error }) (Model, *ModelVersionInfo, error) {
	var (
		model          Model
		metadataBytes  []byte
		defaultVersion sql.NullString
		versionID      sql.NullString
		versionTag     sql.NullString
		versionStatus  sql.NullString
		runnerImage    sql.NullString
		runnerFamily   sql.NullString
		versionCreated sql.NullTime
		versionUpdated sql.NullTime
	)

	err := scanner.Scan(
		&model.ID,
		&model.ExternalID,
		&model.DisplayName,
		&model.Family,
		&model.Description,
		&metadataBytes,
		&model.Status,
		&defaultVersion,
		&model.CreatedAt,
		&model.UpdatedAt,
		&versionID,
		&versionTag,
		&versionStatus,
		&runnerImage,
		&runnerFamily,
		&versionCreated,
		&versionUpdated,
	)
	if err != nil {
		return Model{}, nil, fmt.Errorf("scan model with version: %w", err)
	}

	if len(metadataBytes) > 0 {
		if err := json.Unmarshal(metadataBytes, &model.Metadata); err != nil {
			return Model{}, nil, fmt.Errorf("decode metadata: %w", err)
		}
	} else {
		model.Metadata = map[string]any{}
	}

	if defaultVersion.Valid {
		model.DefaultVersionID = &defaultVersion.String
	}

	if versionID.Valid {
		info := &ModelVersionInfo{
			ID:          versionID.String,
			Version:     versionTag.String,
			Status:      versionStatus.String,
			RunnerImage: runnerImage.String,
		}
		if runnerFamily.Valid {
			value := runnerFamily.String
			info.RunnerFamily = &value
		}
		if versionCreated.Valid {
			info.CreatedAt = versionCreated.Time
		}
		if versionUpdated.Valid {
			info.UpdatedAt = versionUpdated.Time
		}
		return model, info, nil
	}
	return model, nil, nil
}

func scanVersion(scanner interface{ Scan(dest ...any) error }) (ModelVersion, error) {
	var (
		version           ModelVersion
		runnerFamily      sql.NullString
		runnerManifestRaw []byte
		defaultParamsRaw  []byte
		weightsURI        sql.NullString
		minGpuMem         sql.NullInt64
		minVram           sql.NullInt64
		maxBatch          sql.NullInt64
		parametersRaw     []byte
		latencyTier       sql.NullString
		artifactURI       sql.NullString
		capabilities      []sql.NullString
	)

	err := scanner.Scan(
		&version.ID,
		&version.ModelID,
		&version.Version,
		&version.RunnerImage,
		&runnerFamily,
		&runnerManifestRaw,
		&defaultParamsRaw,
		&weightsURI,
		&minGpuMem,
		&minVram,
		&maxBatch,
		&parametersRaw,
		pqArrayScanner(&capabilities),
		&latencyTier,
		&artifactURI,
		&version.Status,
		&version.CreatedAt,
		&version.UpdatedAt,
	)
	if err != nil {
		return ModelVersion{}, fmt.Errorf("scan version: %w", err)
	}

	if weightsURI.Valid {
		version.WeightsURI = &weightsURI.String
	}
	if runnerFamily.Valid {
		value := runnerFamily.String
		version.RunnerFamily = &value
	}
	if len(runnerManifestRaw) > 0 && string(runnerManifestRaw) != "null" {
		if err := json.Unmarshal(runnerManifestRaw, &version.RunnerManifest); err != nil {
			return ModelVersion{}, fmt.Errorf("decode runner manifest: %w", err)
		}
	} else {
		version.RunnerManifest = map[string]any{}
	}
	if len(defaultParamsRaw) > 0 && string(defaultParamsRaw) != "null" {
		if err := json.Unmarshal(defaultParamsRaw, &version.DefaultParameters); err != nil {
			return ModelVersion{}, fmt.Errorf("decode default parameters: %w", err)
		}
	} else {
		version.DefaultParameters = map[string]any{}
	}
	if minGpuMem.Valid {
		v := int(minGpuMem.Int64)
		version.MinGPUMemGB = &v
	}
	if minVram.Valid {
		v := int(minVram.Int64)
		version.MinVRAMGB = &v
	}
	if maxBatch.Valid {
		v := int(maxBatch.Int64)
		version.MaxBatchSize = &v
	}
	if latencyTier.Valid {
		version.LatencyTier = &latencyTier.String
	}
	if artifactURI.Valid {
		version.ArtifactURI = &artifactURI.String
	}

	if len(parametersRaw) > 0 && string(parametersRaw) != "null" {
		if err := json.Unmarshal(parametersRaw, &version.Parameters); err != nil {
			return ModelVersion{}, fmt.Errorf("decode parameters: %w", err)
		}
	} else {
		version.Parameters = map[string]any{}
	}
	if version.RunnerManifest == nil {
		version.RunnerManifest = map[string]any{}
	}
	if version.DefaultParameters == nil {
		version.DefaultParameters = map[string]any{}
	}

	version.Capabilities = flattenNullStringArray(capabilities)

	return version, nil
}

func nullableString(value *string) any {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil
	}
	return trimmed
}

func nullableInt(value *int) any {
	if value == nil {
		return nil
	}
	return *value
}

func pqStringArray(values []string) any {
	if len(values) == 0 {
		return []string{}
	}
	out := make([]string, 0, len(values))
	for _, v := range values {
		trimmed := strings.TrimSpace(v)
		if trimmed == "" {
			continue
		}
		out = append(out, trimmed)
	}
	return out
}

func pqArrayScanner(dest *[]sql.NullString) any {
	return pqArray{dest: dest}
}

// pqArray allows scanning text[] without pulling pgx-specific types.
type pqArray struct {
	dest *[]sql.NullString
}

func (a pqArray) Scan(src any) error {
	switch value := src.(type) {
	case nil:
		*a.dest = nil
		return nil
	case []byte:
		parsed := parsePostgresArray(string(value))
		*a.dest = parsed
		return nil
	case string:
		parsed := parsePostgresArray(value)
		*a.dest = parsed
		return nil
	default:
		return fmt.Errorf("unsupported array type %T", src)
	}
}

func parsePostgresArray(raw string) []sql.NullString {
	raw = strings.Trim(raw, "{}")
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	result := make([]sql.NullString, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		part = strings.Trim(part, "\"")
		result = append(result, sql.NullString{String: part, Valid: true})
	}
	return result
}

func flattenNullStringArray(values []sql.NullString) []string {
	if len(values) == 0 {
		return nil
	}
	out := make([]string, 0, len(values))
	for _, v := range values {
		if v.Valid {
			out = append(out, v.String)
		}
	}
	return out
}

var _ Store = (*PostgresStore)(nil)
