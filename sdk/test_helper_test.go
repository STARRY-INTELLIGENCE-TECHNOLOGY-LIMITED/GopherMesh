package mesh

import (
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"testing"
)

var helperMode = flag.String("gophermesh-helper", "", "internal helper mode")
var helperPort = flag.String("gophermesh-port", "", "internal helper port")
var helperMarker = flag.String("gophermesh-marker", "", "internal helper start marker path")

func TestHelperProcess(t *testing.T) {
	if *helperMode == "" {
		return
	}

	switch *helperMode {
	case "http":
		runHelperHTTPServer(*helperPort, *helperMarker)
	default:
		os.Exit(2)
	}
}

func runHelperHTTPServer(port, markerPath string) {
	ln, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		os.Exit(2)
	}
	defer ln.Close()

	if markerPath != "" {
		file, openErr := os.OpenFile(markerPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if openErr != nil {
			os.Exit(2)
		}
		_, _ = fmt.Fprintln(file, "started")
		_ = file.Close()
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = fmt.Fprintf(w, "helper %s %s", r.Method, r.URL.Path)
	})

	server := &http.Server{Handler: mux}
	if err := server.Serve(ln); err != nil && err != http.ErrServerClosed {
		os.Exit(2)
	}
	os.Exit(0)
}
