package main

import (
    "log"
    "time"
)

func main() {
    log.Println("webhook dispatcher starting (stub)")
    ticker := time.NewTicker(30 * time.Second)
    defer ticker.Stop()

    for t := range ticker.C {
        log.Printf("webhook dispatcher heartbeat at %s", t.Format(time.RFC3339))
    }
}
