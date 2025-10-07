package main

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

//go:embed templates/*
var templates embed.FS

type Config struct {
	RedisURL    string
	MinIOURL    string
	Port        string
}

type JobRequest struct {
	Prompt      string `json:"prompt"`
	ModelID     string `json:"model_id"`
	Steps       int    `json:"steps,omitempty"`
	Guidance    float64 `json:"guidance,omitempty"`
	NumImages   int    `json:"num_images,omitempty"`
}

type JobStatus struct {
	JobID     string    `json:"job_id"`
	ModelID   string    `json:"model_id"`
	Status    string    `json:"status"`
	Prompt    string    `json:"prompt"`
	CreatedAt time.Time `json:"created_at"`
	ImageURL  string    `json:"image_url,omitempty"`
	Error     string    `json:"error,omitempty"`
}

func main() {
	config := Config{
		RedisURL:    getEnv("REDIS_URL", "redis://147.185.41.134:6379"),
		MinIOURL:    getEnv("MINIO_PUBLIC_URL", "http://147.185.41.134:9000"),
		Port:        getEnv("PORT", "8083"),
	}

	// Connect to Redis
	opt, err := redis.ParseURL(config.RedisURL)
	if err != nil {
		log.Fatal(err)
	}
	rdb := redis.NewClient(opt)
	defer rdb.Close()

	// Test connection
	ctx := context.Background()
	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Printf("Warning: Could not connect to Redis: %v", err)
	} else {
		log.Printf("Connected to Redis at %s", config.RedisURL)
	}

	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.RealIP)

	// Parse templates
	tmpl := template.Must(template.ParseFS(templates, "templates/*.html"))

	// Routes
	r.Get("/", func(w http.ResponseWriter, r *http.Request) {
		tmpl.ExecuteTemplate(w, "index.html", config)
	})

	r.Post("/api/jobs", func(w http.ResponseWriter, r *http.Request) {
		var req JobRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		if req.Prompt == "" {
			http.Error(w, "prompt is required", http.StatusBadRequest)
			return
		}

		if req.ModelID == "" {
			req.ModelID = "qwen/image"
		}
		if req.Steps == 0 {
			req.Steps = 20
		}
		if req.Guidance == 0 {
			req.Guidance = 8.5
		}
		if req.NumImages == 0 {
			req.NumImages = 1
		}

		jobID := uuid.New().String()
		queueKey := fmt.Sprintf("queue:%s", req.ModelID)
		jobKey := fmt.Sprintf("job:%s", jobID)

		// Create job data
		jobData := map[string]interface{}{
			"job_id":     jobID,
			"model_id":   req.ModelID,
			"status":     "pending",
			"prompt":     req.Prompt,
			"steps":      req.Steps,
			"guidance":   req.Guidance,
			"num_images": req.NumImages,
			"created_at": time.Now().Unix(),
		}

		// Store job in Redis
		for k, v := range jobData {
			if err := rdb.HSet(ctx, jobKey, k, v).Err(); err != nil {
				http.Error(w, fmt.Sprintf("failed to store job: %v", err), http.StatusInternalServerError)
				return
			}
		}

		// Add to queue
		if err := rdb.RPush(ctx, queueKey, jobID).Err(); err != nil {
			http.Error(w, fmt.Sprintf("failed to queue job: %v", err), http.StatusInternalServerError)
			return
		}

		log.Printf("Created job %s in queue %s", jobID, queueKey)

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"job_id": jobID,
			"status": "pending",
		})
	})

	r.Get("/api/jobs/{jobID}", func(w http.ResponseWriter, r *http.Request) {
		jobID := chi.URLParam(r, "jobID")
		jobKey := fmt.Sprintf("job:%s", jobID)

		exists, err := rdb.Exists(ctx, jobKey).Result()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if exists == 0 {
			http.Error(w, "job not found", http.StatusNotFound)
			return
		}

		jobData, err := rdb.HGetAll(ctx, jobKey).Result()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		status := JobStatus{
			JobID:   jobID,
			ModelID: jobData["model_id"],
			Status:  jobData["status"],
			Prompt:  jobData["prompt"],
		}

		if imageURL, ok := jobData["image_url"]; ok && imageURL != "" {
			status.ImageURL = imageURL
		}

		if errorMsg, ok := jobData["error"]; ok && errorMsg != "" {
			status.Error = errorMsg
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(status)
	})

	r.Get("/api/queue/stats", func(w http.ResponseWriter, r *http.Request) {
		modelID := r.URL.Query().Get("model_id")
		if modelID == "" {
			modelID = "qwen/image"
		}

		queueKey := fmt.Sprintf("queue:%s", modelID)
		queueLen, err := rdb.LLen(ctx, queueKey).Result()
		if err != nil {
			queueLen = 0
		}

		// Get all jobs
		keys, _ := rdb.Keys(ctx, "job:*").Result()

		stats := map[string]interface{}{
			"queue_length": queueLen,
			"total_jobs":   len(keys),
			"model_id":     modelID,
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(stats)
	})

	r.Get("/api/jobs", func(w http.ResponseWriter, r *http.Request) {
		keys, err := rdb.Keys(ctx, "job:*").Result()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		var jobs []JobStatus
		for _, key := range keys {
			jobData, err := rdb.HGetAll(ctx, key).Result()
			if err != nil {
				continue
			}

			status := JobStatus{
				JobID:   jobData["job_id"],
				ModelID: jobData["model_id"],
				Status:  jobData["status"],
				Prompt:  jobData["prompt"],
			}

			if imageURL, ok := jobData["image_url"]; ok && imageURL != "" {
				status.ImageURL = imageURL
			}

			if errorMsg, ok := jobData["error"]; ok && errorMsg != "" {
				status.Error = errorMsg
			}

			jobs = append(jobs, status)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(jobs)
	})

	addr := ":" + config.Port
	log.Printf("Starting admin server on %s", addr)
	log.Printf("Redis: %s", config.RedisURL)
	log.Printf("MinIO: %s", config.MinIOURL)
	log.Fatal(http.ListenAndServe(addr, r))
}

func getEnv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
