package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/redis/go-redis/v9"

	"github.com/vyvo/compute/backend/pkg/modelregistry"
	"github.com/vyvo/compute/backend/pkg/orchestrator"
)

type server struct {
	redisClient  *redis.Client
	orchestrator *orchestrator.Client
	modelStore   modelregistry.Store
	jobDB        *sql.DB
	jobStoreMu   sync.Mutex
}

const jobStoreSchema = `
CREATE TABLE IF NOT EXISTS jobs (
    id UUID PRIMARY KEY,
    model_id TEXT NOT NULL,
    version_id TEXT,
    node_id TEXT,
    status TEXT NOT NULL,
    queue_position INT,
    payload JSONB,
    result JSONB,
    logs JSONB DEFAULT '[]'::jsonb,
    artifacts JSONB DEFAULT '[]'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS jobs_status_idx ON jobs (status);

CREATE TABLE IF NOT EXISTS job_events (
    id BIGSERIAL PRIMARY KEY,
    job_id UUID NOT NULL REFERENCES jobs(id) ON DELETE CASCADE,
    status TEXT NOT NULL,
    message TEXT,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX IF NOT EXISTS job_events_job_id_idx ON job_events (job_id);

ALTER TABLE jobs ADD COLUMN IF NOT EXISTS queue_position INT;
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS payload JSONB;
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS result JSONB;
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS logs JSONB DEFAULT '[]'::jsonb;
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS artifacts JSONB DEFAULT '[]'::jsonb;
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ NOT NULL DEFAULT NOW();
ALTER TABLE jobs ADD COLUMN IF NOT EXISTS updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW();

ALTER TABLE job_events ADD COLUMN IF NOT EXISTS message TEXT;
ALTER TABLE job_events ADD COLUMN IF NOT EXISTS created_at TIMESTAMPTZ NOT NULL DEFAULT NOW();
`

func ensureJobStore(db *sql.DB) error {
	statements := strings.Split(jobStoreSchema, ";")
	for _, stmt := range statements {
		trimmed := strings.TrimSpace(stmt)
		if trimmed == "" {
			continue
		}
		if _, err := db.Exec(trimmed); err != nil {
			return err
		}
	}
	return nil
}

func (s *server) ensureJobStoreReady() error {
	if s.jobDB == nil {
		return fmt.Errorf("job store not configured")
	}

	s.jobStoreMu.Lock()
	defer s.jobStoreMu.Unlock()

	return ensureJobStore(s.jobDB)
}

// Queue monitoring responses
type queueStatsResponse struct {
	Model     string `json:"model"`
	Length    int64  `json:"length"`
	Timestamp int64  `json:"timestamp"`
}

type allQueuesResponse struct {
	Queues    []queueStatsResponse `json:"queues"`
	Timestamp int64                `json:"timestamp"`
}

// Job management responses
type jobSummary struct {
	RequestID string `json:"request_id"`
	ModelID   string `json:"model_id"`
	Status    string `json:"status"`
	CreatedAt int64  `json:"created_at,omitempty"`
	WorkerID  string `json:"worker_id,omitempty"`
	QueuePos  *int   `json:"queue_position,omitempty"`
	UpdatedAt int64  `json:"updated_at,omitempty"`
}

type jobDetail struct {
	ID            string            `json:"id"`
	ModelID       string            `json:"model_id"`
	VersionID     *string           `json:"version_id,omitempty"`
	NodeID        *string           `json:"node_id,omitempty"`
	Status        string            `json:"status"`
	QueuePosition *int              `json:"queue_position,omitempty"`
	Payload       json.RawMessage   `json:"payload,omitempty"`
	Result        json.RawMessage   `json:"result,omitempty"`
	Logs          []string          `json:"logs"`
	Artifacts     []json.RawMessage `json:"artifacts"`
	CreatedAt     time.Time         `json:"created_at"`
	UpdatedAt     time.Time         `json:"updated_at"`
	Events        []jobEvent        `json:"events"`
}

type jobEvent struct {
	Status    string    `json:"status"`
	Message   *string   `json:"message,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

type importModelRequest struct {
	HuggingFaceID string `json:"huggingface_id"`
	RunnerImage   string `json:"runner_image"`
	Version       string `json:"version,omitempty"`
	WeightsFile   string `json:"weights_file,omitempty"`
	Promote       *bool  `json:"promote_version,omitempty"`
}

type importModelResponse struct {
	Model   modelregistry.Model        `json:"model"`
	Version modelregistry.ModelVersion `json:"version"`
}

type huggingFaceModel struct {
	ModelID      string                 `json:"modelId"`
	PipelineTag  string                 `json:"pipeline_tag"`
	Tags         []string               `json:"tags"`
	CardData     map[string]any         `json:"cardData"`
	Siblings     []huggingFaceModelFile `json:"siblings"`
	LibraryName  string                 `json:"library_name"`
	Description  string                 `json:"description"`
	Likes        int                    `json:"likes"`
	Downloads    int                    `json:"downloads"`
	LastModified string                 `json:"lastModified"`
}

type huggingFaceModelFile struct {
	RFilename string `json:"rfilename"`
	Size      int64  `json:"size"`
}

type jobsResponse struct {
	Jobs  []jobSummary `json:"jobs"`
	Total int          `json:"total"`
}

// Redis key management
type redisKeysResponse struct {
	Keys []string `json:"keys"`
}

// Health check
type healthResponse struct {
	Status       string `json:"status"`
	Redis        string `json:"redis"`
	Orchestrator string `json:"orchestrator"`
	Timestamp    int64  `json:"timestamp"`
}

func main() {
	listenAddr := envOrDefault("ADMIN_REST_ADDR", ":8083")
	redisURL := envOrDefault("REDIS_URL", "redis://redis:6379")
	orchestratorURL := envOrDefault("ORCHESTRATOR_URL", "http://orchestrator:8081")

	// Connect to Redis
	opt, err := redis.ParseURL(redisURL)
	if err != nil {
		log.Fatalf("invalid redis URL: %v", err)
	}
	redisClient := redis.NewClient(opt)

	// Test Redis connection
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := redisClient.Ping(ctx).Err(); err != nil {
		log.Printf("WARNING: Redis connection failed: %v", err)
	} else {
		log.Println("Connected to Redis")
	}

	// Create orchestrator client
	orchestratorClient := orchestrator.NewClient(orchestratorURL)

	var (
		modelStore  modelregistry.Store
		storeCloser func()
		jobDB       *sql.DB
	)

	dbURL := strings.TrimSpace(os.Getenv("ADMIN_REST_DATABASE_URL"))
	if dbURL != "" {
		pgStore, err := modelregistry.NewPostgresStore(dbURL)
		if err != nil {
			log.Fatalf("initialise model store: %v", err)
		}
		if err := pgStore.EnsureSchema(); err != nil {
			log.Fatalf("apply model migrations: %v", err)
		}
		modelStore = pgStore
		storeCloser = func() {
			if err := pgStore.Close(); err != nil {
				log.Printf("model store close error: %v", err)
			}
		}
		log.Println("model registry store initialised")
		jobDB, err = sql.Open("pgx", dbURL)
		if err != nil {
			log.Fatalf("connect job database: %v", err)
		}
		jobDB.SetMaxIdleConns(5)
		jobDB.SetMaxOpenConns(15)
		jobDB.SetConnMaxLifetime(30 * time.Minute)
		if err := jobDB.Ping(); err != nil {
			log.Fatalf("job database ping failed: %v", err)
		}
		if err := ensureJobStore(jobDB); err != nil {
			log.Fatalf("apply job store schema: %v", err)
		}
	} else {
		log.Println("ADMIN_REST_DATABASE_URL not set; model registry endpoints disabled")
	}

	if storeCloser != nil {
		defer storeCloser()
	}
	if jobDB != nil {
		defer func() {
			if err := jobDB.Close(); err != nil {
				log.Printf("job database close error: %v", err)
			}
		}()
	}

	srv := &server{
		redisClient:  redisClient,
		orchestrator: orchestratorClient,
		modelStore:   modelStore,
		jobDB:        jobDB,
	}

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(corsMiddleware)

	r.Route("/api", func(r chi.Router) {
		r.Get("/health", srv.handleHealth)

		// Queue monitoring
		r.Route("/queue", func(r chi.Router) {
			r.Get("/", srv.handleListQueues)
			r.Get("/{model}", srv.handleGetQueueStats)
			r.Delete("/{model}", srv.handleClearQueue)
		})

		// Job management
		r.Route("/jobs", func(r chi.Router) {
			r.Get("/", srv.handleListJobs)
			r.Get("/{jobID}", srv.handleGetJob)
			r.Delete("/{jobID}", srv.handleDeleteJob)
			r.Post("/clear-all", srv.handleClearAllJobs)
		})

		// Redis management
		r.Route("/redis", func(r chi.Router) {
			r.Get("/keys", srv.handleListRedisKeys)
			r.Post("/flush", srv.handleFlushRedis)
			r.Get("/info", srv.handleRedisInfo)
		})

		// System operations
		r.Route("/system", func(r chi.Router) {
			r.Post("/wipe", srv.handleWipeAll)
		})

		if srv.modelStore != nil {
			r.Route("/models", func(r chi.Router) {
				r.Get("/", srv.handleListModels)
				r.Post("/", srv.handleCreateModel)
				r.Post("/import", srv.handleImportHuggingFaceModel)
				r.Get("/import/preview", srv.handlePreviewHuggingFaceModel)
				r.Route("/{modelID}", func(r chi.Router) {
					r.Get("/", srv.handleGetModel)
					r.Post("/versions", srv.handleCreateVersion)
					r.Post("/promote", srv.handlePromoteVersion)
					r.Patch("/versions/{versionID}/runner", srv.handleUpdateVersionRunner)
				})
			})
		}
	})

	log.Printf("admin REST listening on %s", listenAddr)
	if err := http.ListenAndServe(listenAddr, r); err != nil {
		log.Fatalf("admin REST failed: %v", err)
	}
}

// Health check
func (s *server) handleHealth(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	resp := healthResponse{
		Status:    "ok",
		Timestamp: time.Now().Unix(),
	}

	// Check Redis
	if err := s.redisClient.Ping(ctx).Err(); err != nil {
		resp.Redis = fmt.Sprintf("error: %v", err)
		resp.Status = "degraded"
	} else {
		resp.Redis = "ok"
	}

	// Check Orchestrator (via simple call)
	// We don't have a health endpoint, so we'll skip detailed check
	resp.Orchestrator = "ok"

	status := http.StatusOK
	if resp.Status == "degraded" {
		status = http.StatusServiceUnavailable
	}

	respondJSON(w, resp, status)
}

// Queue monitoring
func (s *server) handleListQueues(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Get all queue keys
	keys, err := s.redisClient.Keys(ctx, "queue:*").Result()
	if err != nil {
		respondError(w, http.StatusInternalServerError, fmt.Sprintf("failed to list queues: %v", err))
		return
	}

	queues := make([]queueStatsResponse, 0, len(keys))
	for _, key := range keys {
		// Extract model name from key (queue:model-name)
		model := key[6:] // Remove "queue:" prefix

		length, err := s.redisClient.LLen(ctx, key).Result()
		if err != nil {
			log.Printf("failed to get length for queue %s: %v", key, err)
			continue
		}

		queues = append(queues, queueStatsResponse{
			Model:     model,
			Length:    length,
			Timestamp: time.Now().Unix(),
		})
	}

	respondJSON(w, allQueuesResponse{
		Queues:    queues,
		Timestamp: time.Now().Unix(),
	}, http.StatusOK)
}

func (s *server) handleGetQueueStats(w http.ResponseWriter, r *http.Request) {
	model := chi.URLParam(r, "model")
	ctx := r.Context()

	queueKey := fmt.Sprintf("queue:%s", model)
	length, err := s.redisClient.LLen(ctx, queueKey).Result()
	if err != nil {
		respondError(w, http.StatusInternalServerError, fmt.Sprintf("failed to get queue length: %v", err))
		return
	}

	respondJSON(w, queueStatsResponse{
		Model:     model,
		Length:    length,
		Timestamp: time.Now().Unix(),
	}, http.StatusOK)
}

func (s *server) handleClearQueue(w http.ResponseWriter, r *http.Request) {
	model := chi.URLParam(r, "model")
	ctx := r.Context()

	queueKey := fmt.Sprintf("queue:%s", model)
	deleted, err := s.redisClient.Del(ctx, queueKey).Result()
	if err != nil {
		respondError(w, http.StatusInternalServerError, fmt.Sprintf("failed to clear queue: %v", err))
		return
	}

	respondJSON(w, map[string]any{
		"message": fmt.Sprintf("cleared queue for model %s", model),
		"deleted": deleted,
	}, http.StatusOK)
}

// Job management
func (s *server) handleListJobs(w http.ResponseWriter, r *http.Request) {
	if s.jobDB == nil {
		respondError(w, http.StatusServiceUnavailable, "job store not configured")
		return
	}

	if err := s.ensureJobStoreReady(); err != nil {
		respondError(w, http.StatusInternalServerError, fmt.Sprintf("ensure job store: %v", err))
		return
	}

	ctx := r.Context()
	limit := 100
	if v := strings.TrimSpace(r.URL.Query().Get("limit")); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 && parsed <= 500 {
			limit = parsed
		}
	}

	rows, err := s.jobDB.QueryContext(ctx, `
		SELECT id, model_id, version_id, node_id, status, queue_position, created_at, updated_at
		FROM jobs
		ORDER BY created_at DESC
		LIMIT $1
	`, limit)
	if err != nil {
		respondError(w, http.StatusInternalServerError, fmt.Sprintf("query jobs: %v", err))
		return
	}
	defer rows.Close()

	jobs := make([]jobSummary, 0, limit)
	for rows.Next() {
		var (
			id        string
			modelID   string
			versionID sql.NullString
			nodeID    sql.NullString
			status    string
			queuePos  sql.NullInt32
			createdAt time.Time
			updatedAt time.Time
		)
		if err := rows.Scan(&id, &modelID, &versionID, &nodeID, &status, &queuePos, &createdAt, &updatedAt); err != nil {
			log.Printf("scan job row: %v", err)
			continue
		}
		summary := jobSummary{
			RequestID: id,
			ModelID:   modelID,
			Status:    status,
			CreatedAt: createdAt.Unix(),
			UpdatedAt: updatedAt.Unix(),
		}
		if nodeID.Valid {
			summary.WorkerID = nodeID.String
		}
		if queuePos.Valid {
			qp := int(queuePos.Int32)
			summary.QueuePos = &qp
		}
		jobs = append(jobs, summary)
	}

	if err := rows.Err(); err != nil {
		respondError(w, http.StatusInternalServerError, fmt.Sprintf("iterate jobs: %v", err))
		return
	}

	respondJSON(w, jobsResponse{Jobs: jobs, Total: len(jobs)}, http.StatusOK)
}

func (s *server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	if s.jobDB == nil {
		respondError(w, http.StatusServiceUnavailable, "job store not configured")
		return
	}

	if err := s.ensureJobStoreReady(); err != nil {
		respondError(w, http.StatusInternalServerError, fmt.Sprintf("ensure job store: %v", err))
		return
	}

	jobID := chi.URLParam(r, "jobID")
	ctx := r.Context()

	var (
		id             string
		modelID        string
		versionID      sql.NullString
		nodeID         sql.NullString
		status         string
		queuePosition  sql.NullInt32
		payloadBytes   []byte
		resultBytes    []byte
		logsBytes      []byte
		artifactsBytes []byte
		createdAt      time.Time
		updatedAt      time.Time
	)

	err := s.jobDB.QueryRowContext(ctx, `
		SELECT id, model_id, version_id, node_id, status, queue_position, payload, result, logs, artifacts, created_at, updated_at
		FROM jobs
		WHERE id = $1
	`, jobID).Scan(
		&id,
		&modelID,
		&versionID,
		&nodeID,
		&status,
		&queuePosition,
		&payloadBytes,
		&resultBytes,
		&logsBytes,
		&artifactsBytes,
		&createdAt,
		&updatedAt,
	)
	if err == sql.ErrNoRows {
		respondError(w, http.StatusNotFound, "job not found")
		return
	} else if err != nil {
		respondError(w, http.StatusInternalServerError, fmt.Sprintf("query job: %v", err))
		return
	}

	detail := jobDetail{
		ID:        id,
		ModelID:   modelID,
		Status:    status,
		Logs:      []string{},
		Artifacts: []json.RawMessage{},
		CreatedAt: createdAt,
		UpdatedAt: updatedAt,
	}
	if versionID.Valid {
		detail.VersionID = &versionID.String
	}
	if nodeID.Valid {
		detail.NodeID = &nodeID.String
	}
	if queuePosition.Valid {
		qp := int(queuePosition.Int32)
		detail.QueuePosition = &qp
	}
	if len(payloadBytes) > 0 {
		detail.Payload = json.RawMessage(payloadBytes)
	}
	if len(resultBytes) > 0 {
		detail.Result = json.RawMessage(resultBytes)
	}
	if len(logsBytes) > 0 {
		var logs []string
		if err := json.Unmarshal(logsBytes, &logs); err != nil {
			respondError(w, http.StatusInternalServerError, fmt.Sprintf("decode logs: %v", err))
			return
		}
		detail.Logs = logs
	}
	if len(artifactsBytes) > 0 {
		var artifacts []json.RawMessage
		if err := json.Unmarshal(artifactsBytes, &artifacts); err != nil {
			respondError(w, http.StatusInternalServerError, fmt.Sprintf("decode artifacts: %v", err))
			return
		}
		detail.Artifacts = artifacts
	}

	eventRows, err := s.jobDB.QueryContext(ctx, `
		SELECT status, message, created_at
		FROM job_events
		WHERE job_id = $1
		ORDER BY created_at ASC
	`, jobID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, fmt.Sprintf("query job events: %v", err))
		return
	}
	defer eventRows.Close()

	for eventRows.Next() {
		var (
			eventStatus string
			message     sql.NullString
			eventTime   time.Time
		)
		if err := eventRows.Scan(&eventStatus, &message, &eventTime); err != nil {
			respondError(w, http.StatusInternalServerError, fmt.Sprintf("scan job event: %v", err))
			return
		}
		evt := jobEvent{Status: eventStatus, CreatedAt: eventTime}
		if message.Valid {
			evt.Message = &message.String
		}
		detail.Events = append(detail.Events, evt)
	}

	if err := eventRows.Err(); err != nil {
		respondError(w, http.StatusInternalServerError, fmt.Sprintf("iterate job events: %v", err))
		return
	}

	respondJSON(w, detail, http.StatusOK)
}

func (s *server) handleDeleteJob(w http.ResponseWriter, r *http.Request) {
	if s.jobDB == nil {
		respondError(w, http.StatusServiceUnavailable, "job store not configured")
		return
	}

	if err := s.ensureJobStoreReady(); err != nil {
		respondError(w, http.StatusInternalServerError, fmt.Sprintf("ensure job store: %v", err))
		return
	}

	jobID := chi.URLParam(r, "jobID")
	ctx := r.Context()

	res, err := s.jobDB.ExecContext(ctx, `DELETE FROM jobs WHERE id = $1`, jobID)
	if err != nil {
		respondError(w, http.StatusInternalServerError, fmt.Sprintf("delete job: %v", err))
		return
	}
	rowsAffected, err := res.RowsAffected()
	if err != nil {
		respondError(w, http.StatusInternalServerError, fmt.Sprintf("inspect delete result: %v", err))
		return
	}
	if rowsAffected == 0 {
		respondError(w, http.StatusNotFound, "job not found")
		return
	}

	respondJSON(w, map[string]any{
		"message": fmt.Sprintf("deleted job %s", jobID),
		"deleted": rowsAffected,
	}, http.StatusOK)
}

func (s *server) handleClearAllJobs(w http.ResponseWriter, r *http.Request) {
	if s.jobDB == nil {
		respondError(w, http.StatusServiceUnavailable, "job store not configured")
		return
	}

	if err := s.ensureJobStoreReady(); err != nil {
		respondError(w, http.StatusInternalServerError, fmt.Sprintf("ensure job store: %v", err))
		return
	}

	ctx := r.Context()
	res, err := s.jobDB.ExecContext(ctx, `DELETE FROM jobs`)
	if err != nil {
		respondError(w, http.StatusInternalServerError, fmt.Sprintf("clear jobs: %v", err))
		return
	}
	rowsAffected, err := res.RowsAffected()
	if err != nil {
		respondError(w, http.StatusInternalServerError, fmt.Sprintf("inspect clear result: %v", err))
		return
	}

	respondJSON(w, map[string]any{
		"message": fmt.Sprintf("cleared %d jobs", rowsAffected),
		"deleted": rowsAffected,
	}, http.StatusOK)
}

// Redis management
func (s *server) handleListRedisKeys(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	pattern := r.URL.Query().Get("pattern")
	if pattern == "" {
		pattern = "*"
	}

	keys, err := s.redisClient.Keys(ctx, pattern).Result()
	if err != nil {
		respondError(w, http.StatusInternalServerError, fmt.Sprintf("failed to list keys: %v", err))
		return
	}

	respondJSON(w, redisKeysResponse{Keys: keys}, http.StatusOK)
}

func (s *server) handleFlushRedis(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Flush all Redis data (WARNING: destructive!)
	if err := s.redisClient.FlushAll(ctx).Err(); err != nil {
		respondError(w, http.StatusInternalServerError, fmt.Sprintf("failed to flush Redis: %v", err))
		return
	}

	respondJSON(w, map[string]string{
		"message": "Redis flushed successfully",
	}, http.StatusOK)
}

func (s *server) handleRedisInfo(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	info, err := s.redisClient.Info(ctx).Result()
	if err != nil {
		respondError(w, http.StatusInternalServerError, fmt.Sprintf("failed to get Redis info: %v", err))
		return
	}

	respondJSON(w, map[string]string{
		"info": info,
	}, http.StatusOK)
}

// System operations
func (s *server) handleWipeAll(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Wipe all queues and jobs
	queueKeys, _ := s.redisClient.Keys(ctx, "queue:*").Result()
	jobKeys, _ := s.redisClient.Keys(ctx, "job:*").Result()

	allKeys := append(queueKeys, jobKeys...)
	if len(allKeys) > 0 {
		s.redisClient.Del(ctx, allKeys...)
	}

	respondJSON(w, map[string]any{
		"message": "wiped all queues and jobs",
		"deleted": len(allKeys),
	}, http.StatusOK)
}

// Model registry
func (s *server) handleListModels(w http.ResponseWriter, r *http.Request) {
	if s.modelStore == nil {
		respondError(w, http.StatusServiceUnavailable, "model registry not configured")
		return
	}

	var opts modelregistry.QueryOptions
	if families := r.URL.Query()["family"]; len(families) > 0 {
		opts.Family = families
	}
	if statuses := r.URL.Query()["status"]; len(statuses) > 0 {
		opts.Status = statuses
	}

	models, err := s.modelStore.ListModels(opts)
	if err != nil {
		respondError(w, http.StatusInternalServerError, fmt.Sprintf("list models: %v", err))
		return
	}

	respondJSON(w, map[string]any{"models": models}, http.StatusOK)
}

func (s *server) handleCreateModel(w http.ResponseWriter, r *http.Request) {
	if s.modelStore == nil {
		respondError(w, http.StatusServiceUnavailable, "model registry not configured")
		return
	}

	var req createModelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON payload")
		return
	}

	model, err := s.modelStore.CreateModel(req.Model)
	if err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}

	if req.Version != nil {
		version, err := s.modelStore.CreateVersion(model.ID, *req.Version)
		if err != nil {
			respondError(w, http.StatusBadRequest, err.Error())
			return
		}
		if req.PromoteVersion {
			if err := s.modelStore.PromoteVersion(model.ID, version.ID); err != nil {
				respondError(w, http.StatusBadRequest, err.Error())
				return
			}
			model.DefaultVersionID = &version.ID
			model.LatestVersion = &modelregistry.ModelVersionInfo{
				ID:          version.ID,
				Version:     version.Version,
				Status:      version.Status,
				RunnerImage: version.RunnerImage,
				CreatedAt:   version.CreatedAt,
				UpdatedAt:   version.UpdatedAt,
			}
			model.Status = modelregistry.StatusReady
		}
	}

	respondJSON(w, map[string]any{"model": model}, http.StatusCreated)
}

func (s *server) handleImportHuggingFaceModel(w http.ResponseWriter, r *http.Request) {
	if s.modelStore == nil {
		respondError(w, http.StatusServiceUnavailable, "model registry not configured")
		return
	}

	var req importModelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON payload")
		return
	}

	hfID := strings.TrimSpace(req.HuggingFaceID)
	if hfID == "" {
		respondError(w, http.StatusBadRequest, "huggingface_id is required")
		return
	}
	runnerImage := strings.TrimSpace(req.RunnerImage)
	if runnerImage == "" {
		respondError(w, http.StatusBadRequest, "runner_image is required")
		return
	}

	ctx := r.Context()
	hfModel, err := fetchHuggingFaceModel(ctx, hfID)
	if err != nil {
		respondError(w, http.StatusBadGateway, fmt.Sprintf("fetch huggingface model: %v", err))
		return
	}

	displayName := hfModel.ModelID
	if displayName == "" {
		displayName = hfID
	}

	description := firstNonEmpty(
		hfModel.Description,
		stringFromCardData(hfModel.CardData, "summary"),
		stringFromCardData(hfModel.CardData, "description"),
	)
	if description == "" {
		description = fmt.Sprintf("Imported from Hugging Face model %s", hfID)
	}

	family := hfModel.PipelineTag
	if family == "" {
		family = "general"
	}

	metadata := map[string]any{
		"huggingface_id": hfID,
		"pipeline_tag":   hfModel.PipelineTag,
		"library_name":   hfModel.LibraryName,
		"card_data":      hfModel.CardData,
		"likes":          hfModel.Likes,
		"downloads":      hfModel.Downloads,
		"last_modified":  hfModel.LastModified,
	}

	tags := make([]string, 0, len(hfModel.Tags))
	for _, tag := range hfModel.Tags {
		if trimmed := strings.TrimSpace(tag); trimmed != "" {
			tags = append(tags, trimmed)
		}
	}

	model, err := s.modelStore.CreateModel(modelregistry.CreateModelInput{
		ExternalID:  hfID,
		DisplayName: displayName,
		Family:      family,
		Description: description,
		Metadata: map[string]any{
			"huggingface": metadata,
			"tags":        tags,
		},
	})
	if err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}

	versionName := strings.TrimSpace(req.Version)
	if versionName == "" {
		if hfModel.LastModified != "" {
			versionName = hfModel.LastModified
		} else {
			versionName = "imported"
		}
	}

	weightsURL := buildWeightsURL(hfID, req.WeightsFile, hfModel.Siblings)
	var weightsPtr *string
	if weightsURL != "" {
		weightsPtr = &weightsURL
	}

	parameters := map[string]any{
		"source":         "huggingface",
		"huggingface_id": hfID,
	}
	if hfModel.PipelineTag != "" {
		parameters["pipeline_tag"] = hfModel.PipelineTag
	}
	if hfModel.LibraryName != "" {
		parameters["library_name"] = hfModel.LibraryName
	}

	version, err := s.modelStore.CreateVersion(model.ID, modelregistry.CreateVersionInput{
		Version:      versionName,
		RunnerImage:  runnerImage,
		WeightsURI:   weightsPtr,
		Parameters:   parameters,
		Capabilities: deriveCapabilities(hfModel.PipelineTag, tags),
		Status:       modelregistry.StatusReady,
	})
	if err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}

	promote := true
	if req.Promote != nil {
		promote = *req.Promote
	}
	if promote {
		if err := s.modelStore.PromoteVersion(model.ID, version.ID); err != nil {
			respondError(w, http.StatusBadRequest, err.Error())
			return
		}
		model.DefaultVersionID = &version.ID
	}

	respondJSON(w, importModelResponse{Model: model, Version: version}, http.StatusCreated)
}

func (s *server) handlePreviewHuggingFaceModel(w http.ResponseWriter, r *http.Request) {
	hfID := strings.TrimSpace(r.URL.Query().Get("huggingface_id"))
	if hfID == "" {
		respondError(w, http.StatusBadRequest, "huggingface_id is required")
		return
	}

	hfModel, err := fetchHuggingFaceModel(r.Context(), hfID)
	if err != nil {
		respondError(w, http.StatusBadGateway, fmt.Sprintf("fetch huggingface model: %v", err))
		return
	}

	preview := map[string]any{
		"model_id":     hfModel.ModelID,
		"display_name": hfModel.ModelID,
		"description":  firstNonEmpty(hfModel.Description, stringFromCardData(hfModel.CardData, "summary")),
		"pipeline_tag": hfModel.PipelineTag,
		"library_name": hfModel.LibraryName,
		"tags":         hfModel.Tags,
		"suggested_version": func() string {
			if hfModel.LastModified != "" {
				return hfModel.LastModified
			}
			return "imported"
		}(),
		"suggested_weights": buildWeightsURL(hfID, "", hfModel.Siblings),
		"last_modified":     hfModel.LastModified,
		"downloads":         hfModel.Downloads,
		"likes":             hfModel.Likes,
	}

	respondJSON(w, preview, http.StatusOK)
}

func (s *server) handleGetModel(w http.ResponseWriter, r *http.Request) {
	if s.modelStore == nil {
		respondError(w, http.StatusServiceUnavailable, "model registry not configured")
		return
	}

	id := chi.URLParam(r, "modelID")
	model, err := s.modelStore.GetModel(id)
	if err != nil {
		respondError(w, http.StatusNotFound, err.Error())
		return
	}
	respondJSON(w, map[string]any{"model": model}, http.StatusOK)
}

func (s *server) handleCreateVersion(w http.ResponseWriter, r *http.Request) {
	if s.modelStore == nil {
		respondError(w, http.StatusServiceUnavailable, "model registry not configured")
		return
	}

	id := chi.URLParam(r, "modelID")
	var req modelregistry.CreateVersionInput
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON payload")
		return
	}
	version, err := s.modelStore.CreateVersion(id, req)
	if err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	respondJSON(w, map[string]any{"version": version}, http.StatusCreated)
}

func (s *server) handlePromoteVersion(w http.ResponseWriter, r *http.Request) {
	if s.modelStore == nil {
		respondError(w, http.StatusServiceUnavailable, "model registry not configured")
		return
	}

	id := chi.URLParam(r, "modelID")
	var req promoteVersionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON payload")
		return
	}
	if strings.TrimSpace(req.VersionID) == "" {
		respondError(w, http.StatusBadRequest, "version_id is required")
		return
	}
	if err := s.modelStore.PromoteVersion(id, req.VersionID); err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	respondJSON(w, map[string]string{"status": "promoted"}, http.StatusAccepted)
}

func (s *server) handleUpdateVersionRunner(w http.ResponseWriter, r *http.Request) {
	if s.modelStore == nil {
		respondError(w, http.StatusServiceUnavailable, "model registry not configured")
		return
	}

	modelID := chi.URLParam(r, "modelID")
	versionID := chi.URLParam(r, "versionID")
	var req modelregistry.UpdateRunnerConfigInput
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON payload")
		return
	}
	version, err := s.modelStore.UpdateVersionRunnerConfig(modelID, versionID, req)
	if err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}
	respondJSON(w, map[string]any{"version": version}, http.StatusOK)
}

type createModelRequest struct {
	Model          modelregistry.CreateModelInput    `json:"model"`
	Version        *modelregistry.CreateVersionInput `json:"version,omitempty"`
	PromoteVersion bool                              `json:"promote_version"`
}

type promoteVersionRequest struct {
	VersionID string `json:"version_id"`
}

// Helper functions
func getString(m map[string]any, key string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func respondJSON(w http.ResponseWriter, payload any, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func respondError(w http.ResponseWriter, status int, message string) {
	respondJSON(w, map[string]string{"error": message}, status)
}

func envOrDefault(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
		}
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PATCH,DELETE,OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func fetchHuggingFaceModel(ctx context.Context, modelID string) (*huggingFaceModel, error) {
	url := fmt.Sprintf("https://huggingface.co/api/models/%s", modelID)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("model %s not found on Hugging Face", modelID)
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("huggingface API error (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var payload huggingFaceModel
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if payload.ModelID == "" {
		payload.ModelID = modelID
	}
	return &payload, nil
}

func stringFromCardData(cardData map[string]any, key string) string {
	if cardData == nil {
		return ""
	}
	if value, ok := cardData[key]; ok {
		if str, ok := value.(string); ok {
			return str
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func buildWeightsURL(modelID, requested string, files []huggingFaceModelFile) string {
	candidate := strings.TrimSpace(requested)
	if candidate == "" {
		prioritySuffixes := []string{".safetensors", ".bin", ".gguf", ".pt"}
		for _, suffix := range prioritySuffixes {
			for _, file := range files {
				if strings.HasSuffix(strings.ToLower(file.RFilename), suffix) {
					candidate = file.RFilename
					break
				}
			}
			if candidate != "" {
				break
			}
		}
		if candidate == "" && len(files) > 0 {
			candidate = files[0].RFilename
		}
	}

	candidate = strings.TrimSpace(candidate)
	if candidate == "" {
		return ""
	}
	if strings.HasPrefix(candidate, "http://") || strings.HasPrefix(candidate, "https://") {
		return candidate
	}
	candidate = strings.TrimPrefix(candidate, "/")
	return fmt.Sprintf("https://huggingface.co/%s/resolve/main/%s", modelID, candidate)
}

func deriveCapabilities(pipeline string, tags []string) []string {
	capabilities := []string{}
	if pipeline != "" {
		capabilities = append(capabilities, strings.ToUpper(strings.ReplaceAll(pipeline, "-", "_")))
	}
	if len(capabilities) == 0 {
		capabilities = append(capabilities, "GENERAL")
	}
	return capabilities
}
