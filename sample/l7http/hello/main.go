package main

import (
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"time"
)

func main() {
	port := flag.String("port", "19081", "Listen port")
	name := flag.String("name", "http-hello", "Service name")
	flag.Parse()

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"service":   *name,
			"method":    r.Method,
			"path":      r.URL.Path,
			"query":     r.URL.Query(),
			"timestamp": time.Now().UTC().Format(time.RFC3339Nano),
		})
	})
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{
			"service": *name,
			"status":  "ok",
		})
	})

	addr := "127.0.0.1:" + *port
	log.Printf("[%s] listening on %s", *name, addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}
