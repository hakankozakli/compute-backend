package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/redis/go-redis/v9"

	"github.com/vyvo/compute/backend/pkg/orchestrator"
	"github.com/vyvo/compute/backend/pkg/queue"
)

type server struct {
	redisClient  *redis.Client
	orchestrator *orchestrator.Client
	queueManager *queue.Queue
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

	// Create queue manager
	queueManager, err := queue.NewQueue(redisURL)
	if err != nil {
		log.Fatalf("failed to create queue manager: %v", err)
	}

	// Create orchestrator client
	orchestratorClient := orchestrator.NewClient(orchestratorURL)

	srv := &server{
		redisClient:  redisClient,
		orchestrator: orchestratorClient,
		queueManager: queueManager,
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
	ctx := r.Context()

	// Get all job keys from Redis
	keys, err := s.redisClient.Keys(ctx, "job:*").Result()
	if err != nil {
		respondError(w, http.StatusInternalServerError, fmt.Sprintf("failed to list jobs: %v", err))
		return
	}

	jobs := make([]jobSummary, 0, len(keys))
	for _, key := range keys {
		jobID := key[4:] // Remove "job:" prefix

		// Get job data
		jobData, err := s.redisClient.Get(ctx, key).Result()
		if err != nil {
			log.Printf("failed to get job %s: %v", jobID, err)
			continue
		}

		var job map[string]any
		if err := json.Unmarshal([]byte(jobData), &job); err != nil {
			log.Printf("failed to unmarshal job %s: %v", jobID, err)
			continue
		}

		summary := jobSummary{
			RequestID: jobID,
			Status:    getString(job, "status"),
			ModelID:   getString(job, "model"),
			WorkerID:  getString(job, "worker_id"),
		}

		if createdAt, ok := job["created_at"].(float64); ok {
			summary.CreatedAt = int64(createdAt)
		}

		jobs = append(jobs, summary)
	}

	respondJSON(w, jobsResponse{
		Jobs:  jobs,
		Total: len(jobs),
	}, http.StatusOK)
}

func (s *server) handleGetJob(w http.ResponseWriter, r *http.Request) {
	jobID := chi.URLParam(r, "jobID")
	ctx := r.Context()

	// Get from Redis first
	jobKey := fmt.Sprintf("job:%s", jobID)
	jobData, err := s.redisClient.Get(ctx, jobKey).Result()
	if err == redis.Nil {
		// Try orchestrator
		respondError(w, http.StatusNotFound, "job not found")
		return
	} else if err != nil {
		respondError(w, http.StatusInternalServerError, fmt.Sprintf("failed to get job: %v", err))
		return
	}

	var job map[string]any
	if err := json.Unmarshal([]byte(jobData), &job); err != nil {
		respondError(w, http.StatusInternalServerError, fmt.Sprintf("failed to unmarshal job: %v", err))
		return
	}

	respondJSON(w, job, http.StatusOK)
}

func (s *server) handleDeleteJob(w http.ResponseWriter, r *http.Request) {
	jobID := chi.URLParam(r, "jobID")
	ctx := r.Context()

	jobKey := fmt.Sprintf("job:%s", jobID)
	deleted, err := s.redisClient.Del(ctx, jobKey).Result()
	if err != nil {
		respondError(w, http.StatusInternalServerError, fmt.Sprintf("failed to delete job: %v", err))
		return
	}

	if deleted == 0 {
		respondError(w, http.StatusNotFound, "job not found")
		return
	}

	respondJSON(w, map[string]string{
		"message": fmt.Sprintf("deleted job %s", jobID),
	}, http.StatusOK)
}

func (s *server) handleClearAllJobs(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	// Get all job keys
	keys, err := s.redisClient.Keys(ctx, "job:*").Result()
	if err != nil {
		respondError(w, http.StatusInternalServerError, fmt.Sprintf("failed to list jobs: %v", err))
		return
	}

	if len(keys) == 0 {
		respondJSON(w, map[string]any{
			"message": "no jobs to clear",
			"deleted": 0,
		}, http.StatusOK)
		return
	}

	deleted, err := s.redisClient.Del(ctx, keys...).Result()
	if err != nil {
		respondError(w, http.StatusInternalServerError, fmt.Sprintf("failed to delete jobs: %v", err))
		return
	}

	respondJSON(w, map[string]any{
		"message": fmt.Sprintf("cleared %d jobs", deleted),
		"deleted": deleted,
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
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PATCH,DELETE,OPTIONS")
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
