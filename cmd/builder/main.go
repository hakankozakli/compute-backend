package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"

	"github.com/vyvo/compute/backend/pkg/builder"
)

type server struct {
	memStore *builder.MemStore
	pgStore  *builder.PostgresStore
}

func main() {
	addr := envOrDefault("BUILDER_ADDR", ":8085")

	srv := &server{memStore: builder.NewMemStore()}

	if dsn := os.Getenv("BUILDER_DATABASE_URL"); strings.TrimSpace(dsn) != "" {
		pg, err := builder.NewPostgresStore(dsn)
		if err != nil {
			log.Fatalf("builder postgres init failed: %v", err)
		}
		srv.pgStore = pg
		defer func() {
			if err := pg.Close(); err != nil {
				log.Printf("builder postgres close error: %v", err)
			}
		}()
	}

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	r.Route("/api", func(r chi.Router) {
		r.Post("/builds", srv.handleCreateBuild)
		r.Get("/builds", srv.handleListBuilds)
		r.Route("/builds/{buildID}", func(r chi.Router) {
			r.Get("/", srv.handleGetBuild)
			r.Get("/logs", srv.handleStreamLogs)
		})
	})

	log.Printf("builder service listening on %s", addr)
	if err := http.ListenAndServe(addr, r); err != nil && err != http.ErrServerClosed {
		log.Fatalf("builder service failed: %v", err)
	}
}

func (s *server) handleCreateBuild(w http.ResponseWriter, r *http.Request) {
	var payload builder.CreateRequest
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		respondError(w, http.StatusBadRequest, "invalid JSON payload")
		return
	}

	if payload.Template == "" || payload.RunnerName == "" || payload.ModelID == "" || payload.Version == "" || payload.Repository == "" || payload.RunnerImage == "" {
		respondError(w, http.StatusBadRequest, "template, runner_name, model_id, version, repository, and runner_image are required")
		return
	}

	id := uuid.NewString()
	now := time.Now().UTC()
	build := builder.Build{
		ID:          id,
		Template:    payload.Template,
		RunnerName:  payload.RunnerName,
		ModelID:     payload.ModelID,
		Version:     payload.Version,
		Repository:  payload.Repository,
		RunnerImage: payload.RunnerImage,
		WeightsURI:  payload.WeightsURI,
		Status:      builder.StatusQueued,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	s.memStore.Create(build)
	if s.pgStore != nil {
		if err := s.pgStore.Create(build); err != nil {
			log.Printf("persist build failed: %v", err)
		}
	}
	respondJSON(w, map[string]any{"build": build}, http.StatusAccepted)

	go s.runBuild(build, payload)
}

func (s *server) runBuild(build builder.Build, payload builder.CreateRequest) {
	s.appendLog(build.ID, fmt.Sprintf("queued build for %s:%s", payload.RunnerName, payload.Version))
	s.updateStatus(build.ID, builder.StatusRunning, "")

	command := strings.TrimSpace(os.Getenv("BUILDER_BUILD_COMMAND"))
	if command == "" {
		// Fallback simulation when no command provided.
		steps := []string{
			"cloning repository",
			"building docker image",
			"pushing image to registry",
			"updating model registry",
		}
		for _, step := range steps {
			s.appendLog(build.ID, step)
			time.Sleep(1 * time.Second)
		}
		s.updateStatus(build.ID, builder.StatusSucceeded, "")
		s.appendLog(build.ID, "build completed successfully")
		s.memStore.CloseSubscribers(build.ID)
		s.notifyRegistry(build, payload)
		return
	}

	workdir := os.Getenv("BUILDER_BUILD_WORKDIR")
	cmd := exec.Command("/bin/sh", "-c", command)
	if strings.TrimSpace(workdir) != "" {
		cmd.Dir = workdir
	}
	env := os.Environ()
	for key, value := range payload.Env {
		env = append(env, fmt.Sprintf("%s=%s", key, value))
	}
	env = append(env,
		fmt.Sprintf("RUNNER_NAME=%s", payload.RunnerName),
		fmt.Sprintf("MODEL_ID=%s", payload.ModelID),
		fmt.Sprintf("RUNNER_VERSION=%s", payload.Version),
		fmt.Sprintf("RUNNER_IMAGE=%s", payload.RunnerImage),
	)
	cmd.Env = env
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		s.failBuild(build.ID, fmt.Sprintf("stdout pipe error: %v", err))
		return
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		s.failBuild(build.ID, fmt.Sprintf("stderr pipe error: %v", err))
		return
	}

	if err := cmd.Start(); err != nil {
		s.failBuild(build.ID, fmt.Sprintf("build command start failed: %v", err))
		return
	}

	done := make(chan struct{})
	go s.streamPipe(build.ID, stdout, done)
	go s.streamPipe(build.ID, stderr, done)

	if err := cmd.Wait(); err != nil {
		s.failBuild(build.ID, fmt.Sprintf("build command failed: %v", err))
		s.memStore.CloseSubscribers(build.ID)
		return
	}

	// Wait for stream goroutines to finish draining
	<-done
	<-done

	s.updateStatus(build.ID, builder.StatusSucceeded, "")
	s.appendLog(build.ID, "build completed successfully")
	s.memStore.CloseSubscribers(build.ID)
	s.notifyRegistry(build, payload)
}

func (s *server) streamPipe(buildID string, pipe io.Reader, done chan<- struct{}) {
	scanner := bufio.NewScanner(pipe)
	for scanner.Scan() {
		s.appendLog(buildID, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		s.appendLog(buildID, fmt.Sprintf("log stream error: %v", err))
	}
	done <- struct{}{}
}

func (s *server) updateStatus(id string, status builder.Status, errMsg string) {
	finished := status == builder.StatusSucceeded || status == builder.StatusFailed
	finishedAt := time.Now().UTC()
	if _, err := s.memStore.SetStatus(id, status, finished, finishedAt, errMsg); err != nil {
		log.Printf("memory status error: %v", err)
	}
	if s.pgStore != nil {
		var fPtr *time.Time
		if finished {
			fPtr = &finishedAt
		}
		if err := s.pgStore.UpdateStatus(id, status, fPtr, errMsg); err != nil {
			log.Printf("postgres status error: %v", err)
		}
	}
}

func (s *server) failBuild(id string, message string) {
	s.appendLog(id, message)
	s.updateStatus(id, builder.StatusFailed, message)
	s.memStore.CloseSubscribers(id)
}

func (s *server) appendLog(id string, line string) {
	s.memStore.AppendLog(id, line)
	if s.pgStore != nil {
		if err := s.pgStore.AppendLog(id, line); err != nil {
			log.Printf("persist log error: %v", err)
		}
	}
}

func (s *server) notifyRegistry(build builder.Build, payload builder.CreateRequest) {
	base := strings.TrimSpace(os.Getenv("BUILDER_REGISTRY_URL"))
	if base == "" {
		return
	}
	modelPath := url.PathEscape(payload.ModelID)
	endpoint := fmt.Sprintf("%s/api/models/%s/versions", strings.TrimSuffix(base, "/"), modelPath)
	body := map[string]any{
		"version":      payload.Version,
		"runner_image": payload.RunnerImage,
		"weights_uri":  payload.WeightsURI,
		"status":       "READY",
	}
	payloadBytes, err := json.Marshal(body)
	if err != nil {
		log.Printf("registry marshal error for build %s: %v", build.ID, err)
		return
	}
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(payloadBytes))
	if err != nil {
		log.Printf("registry request error for build %s: %v", build.ID, err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("registry call failed for build %s: %v", build.ID, err)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		log.Printf("registry call returned status %d for build %s", resp.StatusCode, build.ID)
	}
}

func (s *server) handleListBuilds(w http.ResponseWriter, r *http.Request) {
	if s.pgStore != nil {
		builds, err := s.pgStore.List()
		if err != nil {
			respondError(w, http.StatusInternalServerError, err.Error())
			return
		}
		respondJSON(w, map[string]any{"builds": builds}, http.StatusOK)
		return
	}
	respondJSON(w, map[string]any{"builds": s.memStore.List()}, http.StatusOK)
}

func (s *server) handleGetBuild(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "buildID")
	var (
		build builder.Build
		err   error
	)
	if s.pgStore != nil {
		build, err = s.pgStore.Get(id)
	} else {
		build, err = s.memStore.Get(id)
	}
	if err != nil {
		respondError(w, http.StatusNotFound, err.Error())
		return
	}
	respondJSON(w, map[string]any{"build": build}, http.StatusOK)
}

func (s *server) handleStreamLogs(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "buildID")
	ch, err := s.memStore.Subscribe(id)
	if err != nil {
		respondError(w, http.StatusNotFound, err.Error())
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")

	flusher, ok := w.(http.Flusher)
	if !ok {
		respondError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	done := r.Context().Done()

	for {
		select {
		case <-done:
			return
		case msg, ok := <-ch:
			if !ok {
				fmt.Fprintf(w, "data: %s\n\n", "[stream closed]")
				flusher.Flush()
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", msg)
			flusher.Flush()
		}
	}
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
