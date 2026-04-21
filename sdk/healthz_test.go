package mesh

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBuildHealthzPayloadUsesDefaultVersionAndCustomFields(t *testing.T) {
	t.Parallel()

	payload := BuildHealthzPayload(HealthzOptions{
		Fields: map[string]any{
			"name": "etaiIotPlugin",
		},
	})

	if got := payload["ok"]; got != true {
		t.Fatalf("payload ok = %#v, want true", got)
	}
	if got := payload["status"]; got != "ok" {
		t.Fatalf("payload status = %#v, want %q", got, "ok")
	}
	if got := payload["mesh"]; got != "running" {
		t.Fatalf("payload mesh = %#v, want %q", got, "running")
	}
	if got := payload["version"]; got != DefaultHealthzVersion {
		t.Fatalf("payload version = %#v, want %q", got, DefaultHealthzVersion)
	}
	if got := payload["name"]; got != "etaiIotPlugin" {
		t.Fatalf("payload name = %#v, want %q", got, "etaiIotPlugin")
	}
}

func TestBuildHealthzPayloadKeepsReservedFieldsStable(t *testing.T) {
	t.Parallel()

	payload := BuildHealthzPayload(HealthzOptions{
		Version: "1.2.3",
		Fields: map[string]any{
			"version": "9.9.9",
			"status":  "bad",
			"mesh":    "stopped",
			"ok":      false,
			"name":    "custom",
		},
	})

	if got := payload["version"]; got != "1.2.3" {
		t.Fatalf("payload version = %#v, want %q", got, "1.2.3")
	}
	if got := payload["status"]; got != "ok" {
		t.Fatalf("payload status = %#v, want %q", got, "ok")
	}
	if got := payload["mesh"]; got != "running" {
		t.Fatalf("payload mesh = %#v, want %q", got, "running")
	}
	if got := payload["ok"]; got != true {
		t.Fatalf("payload ok = %#v, want true", got)
	}
	if got := payload["name"]; got != "custom" {
		t.Fatalf("payload name = %#v, want %q", got, "custom")
	}
}

func TestWriteHealthzWritesJSONResponse(t *testing.T) {
	t.Parallel()

	rec := httptest.NewRecorder()
	WriteHealthz(rec, HealthzOptions{
		Version: "0.4.0",
		Fields: map[string]any{
			"name": "etaiIotPlugin",
		},
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if got := rec.Header().Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want %q", got, "application/json")
	}

	var payload map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("json.Unmarshal() error = %v body=%s", err, rec.Body.String())
	}
	if got := payload["version"]; got != "0.4.0" {
		t.Fatalf("payload version = %#v, want %q", got, "0.4.0")
	}
	if got := payload["name"]; got != "etaiIotPlugin" {
		t.Fatalf("payload name = %#v, want %q", got, "etaiIotPlugin")
	}
}
