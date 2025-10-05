package main

import (
    "bufio"
    "context"
    "encoding/json"
    "errors"
    "fmt"
    "io"
    "log"
    "net/http"
    "os/signal"
    "strings"
    "syscall"
    "time"

    "github.com/go-chi/chi/v5"
    "github.com/go-chi/chi/v5/middleware"

    "github.com/vyvo/compute/backend/pkg/config"
    "github.com/vyvo/compute/backend/pkg/falcompat"
    orchestrator "github.com/vyvo/compute/backend/pkg/orchestrator"
)

type server struct {
    cfg          config.GatewayConfig
    orchestrator *orchestrator.Client
    queueBase    string
}

func newServer(cfg config.GatewayConfig) *server {
    base := strings.TrimSuffix(cfg.QueueBase, "/")
    return &server{
        cfg:          cfg,
        orchestrator: orchestrator.NewClient(cfg.OrchestratorURL),
        queueBase:    base,
    }
}

func main() {
    cfg, err := config.LoadGateway()
    if err != nil {
        log.Fatalf("failed to load config: %v", err)
    }

    srv := newServer(cfg)

    ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
    defer stop()

    router := chi.NewRouter()
    router.Use(middleware.RequestID)
    router.Use(middleware.RealIP)
    router.Use(middleware.Logger)
    router.Use(middleware.Recoverer)
    router.Use(timeoutMiddleware(60 * time.Second))

    router.Get("/healthz", healthzHandler)

    router.Route("/sync", func(r chi.Router) {
        r.Post("/*", srv.handleSyncInvoke)
    })

    router.Post("/*", srv.handleQueueSubmission)
    router.Get("/*", srv.handleGetRequests)
    router.Put("/*", srv.handleMutations)

    httpSrv := &http.Server{
        Addr:    cfg.ListenAddr,
        Handler: router,
    }

    go func() {
        <-ctx.Done()
        shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
        defer cancel()
        if err := httpSrv.Shutdown(shutdownCtx); err != nil {
            log.Printf("gateway shutdown error: %v", err)
        }
    }()

    log.Printf("gateway listening on %s", cfg.ListenAddr)
    if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
        log.Fatalf("gateway listen failed: %v", err)
    }

    <-ctx.Done()
    log.Println("gateway stopped")
}

func timeoutMiddleware(timeout time.Duration) func(http.Handler) http.Handler {
    return func(next http.Handler) http.Handler {
        return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
            ctx, cancel := context.WithTimeout(r.Context(), timeout)
            defer cancel()
            next.ServeHTTP(w, r.WithContext(ctx))
        })
    }
}

func healthzHandler(w http.ResponseWriter, r *http.Request) {
    w.Header().Set("Content-Type", "application/json")
    _ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *server) handleSyncInvoke(w http.ResponseWriter, r *http.Request) {
    modelPath := strings.Trim(r.URL.Path, "/")
    modelPath = strings.TrimPrefix(modelPath, "sync/")
    if modelPath == "" {
        http.NotFound(w, r)
        return
    }

    payload, err := io.ReadAll(r.Body)
    if err != nil {
        http.Error(w, "failed to read body", http.StatusBadRequest)
        return
    }

    resp, err := s.orchestrator.InvokeSync(r.Context(), orchestrator.SyncRequest{
        ModelID: modelPath,
        Payload: json.RawMessage(payload),
    })
    if err != nil {
        http.Error(w, fmt.Sprintf("sync invoke failed: %v", err), http.StatusBadGateway)
        return
    }

    w.Header().Set("Content-Type", "application/json")
    if resp.Result == nil {
        resp.Result = json.RawMessage(`{}`)
    }
    w.WriteHeader(http.StatusOK)
    _, _ = w.Write(resp.Result)
}

func (s *server) handleQueueSubmission(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPost {
        http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
        return
    }
    modelPath := strings.Trim(r.URL.Path, "/")
    if modelPath == "" {
        http.NotFound(w, r)
        return
    }

    body, err := io.ReadAll(r.Body)
    if err != nil {
        http.Error(w, "failed to read body", http.StatusBadRequest)
        return
    }

    storeIO := !strings.EqualFold(r.Header.Get("X-Fal-Store-IO"), "0")
    webhook := r.URL.Query().Get("fal_webhook")
    var webhookPtr *string
    if strings.TrimSpace(webhook) != "" {
        webhookPtr = &webhook
    }

    submitResp, err := s.orchestrator.SubmitJob(r.Context(), orchestrator.SubmitJobRequest{
        ModelID:    modelPath,
        Payload:    json.RawMessage(body),
        StoreIO:    storeIO,
        FalWebhook: webhookPtr,
    })
    if err != nil {
        http.Error(w, fmt.Sprintf("queue submit failed: %v", err), http.StatusBadGateway)
        return
    }

    responseURL := s.responseURL(modelPath, submitResp.RequestID)
    envelope := falcompat.SubmissionEnvelope{
        RequestID:   submitResp.RequestID,
        ResponseURL: responseURL,
        StatusURL:   responseURL + "/status",
        CancelURL:   responseURL + "/cancel",
    }

    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(http.StatusCreated)
    _ = json.NewEncoder(w).Encode(envelope)
}

func (s *server) handleGetRequests(w http.ResponseWriter, r *http.Request) {
    path := strings.Trim(r.URL.Path, "/")
    modelPath, requestID, suffix, ok := splitRequestPath(path)
    if !ok {
        http.NotFound(w, r)
        return
    }

    switch suffix {
    case "":
        s.handleGetResponse(w, r, modelPath, requestID)
    case "status":
        s.handleStatus(w, r, modelPath, requestID)
    case "status/stream":
        s.handleStatusStream(w, r, modelPath, requestID)
    default:
        http.NotFound(w, r)
    }
}

func (s *server) handleMutations(w http.ResponseWriter, r *http.Request) {
    if r.Method != http.MethodPut {
        http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
        return
    }

    path := strings.Trim(r.URL.Path, "/")
    modelPath, requestID, suffix, ok := splitRequestPath(path)
    if !ok || suffix != "cancel" {
        http.NotFound(w, r)
        return
    }

    cancelResp, err := s.orchestrator.CancelJob(r.Context(), requestID)
    if err != nil {
        if errors.Is(err, orchestrator.ErrNotFound) {
            http.NotFound(w, r)
            return
        }
        http.Error(w, fmt.Sprintf("cancel failed: %v", err), http.StatusBadGateway)
        return
    }

    statusCode := http.StatusAccepted
    if cancelResp.Status != "CANCELLATION_REQUESTED" {
        statusCode = http.StatusBadRequest
    }

    w.Header().Set("Content-Type", "application/json")
    w.WriteHeader(statusCode)
    _ = json.NewEncoder(w).Encode(cancelResp)
}

func (s *server) handleStatus(w http.ResponseWriter, r *http.Request, modelPath, requestID string) {
    job, err := s.orchestrator.GetJob(r.Context(), requestID)
    if err != nil {
        if errors.Is(err, orchestrator.ErrNotFound) {
            http.NotFound(w, r)
            return
        }
        http.Error(w, fmt.Sprintf("status lookup failed: %v", err), http.StatusBadGateway)
        return
    }

    statusResp := mapJobToStatus(job, s.responseURL(modelPath, requestID))

    w.Header().Set("Content-Type", "application/json")
    _ = json.NewEncoder(w).Encode(statusResp)
}

func (s *server) handleGetResponse(w http.ResponseWriter, r *http.Request, modelPath, requestID string) {
    job, err := s.orchestrator.GetJob(r.Context(), requestID)
    if err != nil {
        if errors.Is(err, orchestrator.ErrNotFound) {
            http.NotFound(w, r)
            return
        }
        http.Error(w, fmt.Sprintf("response lookup failed: %v", err), http.StatusBadGateway)
        return
    }

    switch falcompat.QueueStatus(job.Status) {
    case falcompat.StatusCompleted:
        if job.Result == nil {
            http.Error(w, "missing result payload", http.StatusInternalServerError)
            return
        }
        w.Header().Set("Content-Type", "application/json")
        w.WriteHeader(http.StatusOK)
        _, _ = w.Write(job.Result)
    case falcompat.StatusInQueue, falcompat.StatusInProgress:
        w.Header().Set("Content-Type", "application/json")
        w.WriteHeader(http.StatusAccepted)
        _ = json.NewEncoder(w).Encode(mapJobToStatus(job, s.responseURL(modelPath, requestID)))
    case falcompat.StatusError:
        http.Error(w, "job failed", http.StatusBadGateway)
    default:
        http.Error(w, "job not ready", http.StatusBadRequest)
    }
}

func (s *server) handleStatusStream(w http.ResponseWriter, r *http.Request, modelPath, requestID string) {
    resp, err := s.orchestrator.StreamStatus(r.Context(), requestID)
    if err != nil {
        http.Error(w, fmt.Sprintf("status stream failed: %v", err), http.StatusBadGateway)
        return
    }
    defer resp.Body.Close()

    w.Header().Set("Content-Type", "text/event-stream")
    w.Header().Set("Cache-Control", "no-cache")
    w.Header().Set("Connection", "keep-alive")

    flusher, ok := w.(http.Flusher)
    if !ok {
        http.Error(w, "streaming unsupported", http.StatusInternalServerError)
        return
    }

    writer := bufio.NewWriter(w)
    err = orchestrator.ReadEvents(resp.Body, func(payload json.RawMessage) error {
        var job orchestrator.JobDetails
        if err := json.Unmarshal(payload, &job); err != nil {
            return fmt.Errorf("decode event payload: %w", err)
        }
        status := mapJobToStatus(job, s.responseURL(modelPath, requestID))
        bytes, err := json.Marshal(status)
        if err != nil {
            return fmt.Errorf("marshal status: %w", err)
        }
        if _, err := writer.WriteString("data: "); err != nil {
            return err
        }
        if _, err := writer.Write(bytes); err != nil {
            return err
        }
        if _, err := writer.WriteString("\n\n"); err != nil {
            return err
        }
        if err := writer.Flush(); err != nil {
            return err
        }
        flusher.Flush()
        return nil
    })

    if err != nil && !errors.Is(err, context.Canceled) {
        log.Printf("status stream error: %v", err)
    }
}

func mapJobToStatus(job orchestrator.JobDetails, responseURL string) falcompat.StatusResponse {
    status := falcompat.QueueStatus(job.Status)
    var queuePos *int
    if status == falcompat.StatusInQueue {
        queuePos = job.QueuePosition
    }
    return falcompat.StatusResponse{
        Status:        status,
        QueuePosition: queuePos,
        ResponseURL:   responseURL,
        Logs:          job.Logs,
    }
}

func splitRequestPath(path string) (string, string, string, bool) {
    idx := strings.Index(path, "/requests/")
    if idx == -1 {
        return "", "", "", false
    }
    modelPath := path[:idx]
    remainder := path[idx+len("/requests/"):]
    parts := strings.Split(remainder, "/")
    if len(parts) == 0 {
        return "", "", "", false
    }
    requestID := parts[0]
    suffix := strings.Join(parts[1:], "/")
    return modelPath, requestID, suffix, true
}

func (s *server) responseURL(modelPath, requestID string) string {
    base := s.queueBase
    if base == "" {
        base = "https://queue.vyvo.local"
    }
    return fmt.Sprintf("%s/%s/requests/%s", strings.TrimSuffix(base, "/"), modelPath, requestID)
}
