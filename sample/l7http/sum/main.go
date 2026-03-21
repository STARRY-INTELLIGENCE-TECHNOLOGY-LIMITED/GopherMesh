package main

import (
	"encoding/json"
	"flag"
	"log"
	"net/http"
	"strconv"
)

func main() {
	port := flag.String("port", "19082", "Listen port")
	name := flag.String("name", "http-sum", "Service name")
	flag.Parse()

	mux := http.NewServeMux()
	mux.HandleFunc("/sum", func(w http.ResponseWriter, r *http.Request) {
		a, err := strconv.Atoi(r.URL.Query().Get("a"))
		if err != nil {
			http.Error(w, "invalid query parameter a", http.StatusBadRequest)
			return
		}

		b, err := strconv.Atoi(r.URL.Query().Get("b"))
		if err != nil {
			http.Error(w, "invalid query parameter b", http.StatusBadRequest)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"service": *name,
			"a":       a,
			"b":       b,
			"sum":     a + b,
		})
	})
	mux.HandleFunc("/headers", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"service":    *name,
			"user_agent": r.UserAgent(),
			"origin":     r.Header.Get("Origin"),
		})
	})

	addr := "127.0.0.1:" + *port
	log.Printf("[%s] listening on %s", *name, addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}
