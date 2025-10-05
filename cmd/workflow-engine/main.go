package main

import (
    "context"
    "log"
    "net/http"
    "os"
    "time"
)

func main() {
    ctx, stop := context.WithCancel(context.Background())
    defer stop()

    mux := http.NewServeMux()
    mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
        w.WriteHeader(http.StatusOK)
        _, _ = w.Write([]byte("{\"status\":\"ok\"}"))
    })

    addr := valueOrDefault(os.Getenv("WORKFLOW_ENGINE_ADDR"), ":8082")
    srv := &http.Server{Addr: addr, Handler: mux}

    go func() {
        <-ctx.Done()
        shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
        defer cancel()
        _ = srv.Shutdown(shutdownCtx)
    }()

    log.Printf("workflow engine listening on %s", addr)
    if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
        log.Fatalf("workflow engine failed: %v", err)
    }
}

func valueOrDefault(val, fallback string) string {
    if val == "" {
        return fallback
    }
    return val
}
