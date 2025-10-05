package main

import (
    "encoding/json"
    "log"
    "net/http"
)

func main() {
    mux := http.NewServeMux()
    mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Content-Type", "application/json")
        _ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
    })

    log.Println("admin REST listening on :8083")
    if err := http.ListenAndServe(":8083", mux); err != nil {
        log.Fatalf("admin REST failed: %v", err)
    }
}
