package mesh

import (
	"encoding/json"
	"net/http"
	"strings"
)

// DefaultHealthzVersion is used when callers do not specify a version.
const DefaultHealthzVersion = "0.0.1"

// HealthzOptions controls the payload returned by SDK health probes.
type HealthzOptions struct {
	Version string
	Fields  map[string]any
}

var reservedHealthzFields = map[string]struct{}{
	"ok":      {},
	"status":  {},
	"mesh":    {},
	"version": {},
}

// BuildHealthzPayload returns the canonical SDK health payload with optional
// custom fields appended. Reserved keys remain under SDK control.
func BuildHealthzPayload(options HealthzOptions) map[string]any {
	normalized := normalizeHealthzOptions(options)
	payload := map[string]any{
		"ok":      true,
		"status":  "ok",
		"mesh":    "running",
		"version": normalized.Version,
	}

	for key, value := range normalized.Fields {
		payload[key] = value
	}

	return payload
}

// WriteHealthz writes the canonical SDK health payload as JSON.
func WriteHealthz(w http.ResponseWriter, options HealthzOptions) {
	body, err := json.Marshal(BuildHealthzPayload(options))
	if err != nil {
		http.Error(w, "failed to encode healthz payload", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(body)
}

func normalizeHealthzOptions(options HealthzOptions) HealthzOptions {
	version := strings.TrimSpace(options.Version)
	if version == "" {
		version = DefaultHealthzVersion
	}

	fields := make(map[string]any, len(options.Fields))
	for key, value := range options.Fields {
		normalizedKey := strings.TrimSpace(key)
		if normalizedKey == "" {
			continue
		}
		if _, reserved := reservedHealthzFields[strings.ToLower(normalizedKey)]; reserved {
			continue
		}
		fields[normalizedKey] = value
	}

	return HealthzOptions{
		Version: version,
		Fields:  fields,
	}
}
