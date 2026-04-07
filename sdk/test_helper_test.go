package mesh

import (
	"bytes"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

var helperMode = flag.String("gophermesh-helper", "", "internal helper mode")
var helperPort = flag.String("gophermesh-port", "", "internal helper port")
var helperMarker = flag.String("gophermesh-marker", "", "internal helper start marker path")
var helperSTDIOPrefix = flag.String("gophermesh-stdio-prefix", "", "internal helper stdio response prefix")
var helperSTDIOStderr = flag.String("gophermesh-stdio-stderr", "", "internal helper stdio stderr line")
var helperSTDIOChunks = flag.String("gophermesh-stdio-chunks", "", "internal helper stdio response chunks separated by |")
var helperSTDIODelayMS = flag.Int("gophermesh-stdio-delay-ms", 0, "internal helper stdio delay between chunks")
var helperSTDIOReadPrefixBytes = flag.Int("gophermesh-stdio-read-prefix-bytes", 0, "internal helper stdio bytes to read before emitting an early stdout chunk")
var helperSTDIOEarlyChunkBytes = flag.Int("gophermesh-stdio-early-chunk-bytes", 0, "internal helper stdio bytes to emit to stdout before stdin reaches EOF")
var helperSTDIOFinalChunk = flag.String("gophermesh-stdio-final-chunk", "", "internal helper stdio final stdout chunk written after stdin drains")

func TestHelperProcess(t *testing.T) {
	if *helperMode == "" {
		return
	}

	switch *helperMode {
	case "http":
		runHelperHTTPServer(*helperPort, *helperMarker)
	case "stdio":
		runHelperSTDIO(
			*helperMarker,
			*helperSTDIOPrefix,
			*helperSTDIOStderr,
			*helperSTDIOChunks,
			*helperSTDIODelayMS,
			*helperSTDIOReadPrefixBytes,
			*helperSTDIOEarlyChunkBytes,
			*helperSTDIOFinalChunk,
		)
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

func runHelperSTDIO(markerPath, prefix, stderrLine, chunkSpec string, delayMS, readPrefixBytes, earlyChunkBytes int, finalChunk string) {
	if markerPath != "" {
		file, openErr := os.OpenFile(markerPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
		if openErr != nil {
			os.Exit(2)
		}
		_, _ = fmt.Fprintln(file, "started")
		_ = file.Close()
	}

	if stderrLine != "" {
		if _, err := fmt.Fprintln(os.Stderr, stderrLine); err != nil {
			os.Exit(2)
		}
	}

	if readPrefixBytes > 0 || earlyChunkBytes > 0 || strings.TrimSpace(finalChunk) != "" {
		if readPrefixBytes > 0 {
			prefixBuf := make([]byte, readPrefixBytes)
			if _, err := io.ReadFull(os.Stdin, prefixBuf); err != nil {
				os.Exit(2)
			}
		}
		if earlyChunkBytes > 0 {
			if _, err := os.Stdout.Write(bytes.Repeat([]byte("E"), earlyChunkBytes)); err != nil {
				os.Exit(2)
			}
		}
		if _, err := io.Copy(io.Discard, os.Stdin); err != nil {
			os.Exit(2)
		}
		if strings.TrimSpace(finalChunk) != "" {
			if _, err := io.WriteString(os.Stdout, decodeHelperChunk(finalChunk)); err != nil {
				os.Exit(2)
			}
		}
		os.Exit(0)
	}

	payload, err := io.ReadAll(os.Stdin)
	if err != nil {
		os.Exit(2)
	}

	if strings.TrimSpace(chunkSpec) != "" {
		chunks := strings.Split(chunkSpec, "|")
		for i, chunk := range chunks {
			if _, err := io.WriteString(os.Stdout, decodeHelperChunk(chunk)); err != nil {
				os.Exit(2)
			}
			if delayMS > 0 && i < len(chunks)-1 {
				time.Sleep(time.Duration(delayMS) * time.Millisecond)
			}
		}
		os.Exit(0)
	}

	if _, err := fmt.Fprint(os.Stdout, prefix+string(payload)); err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}

func decodeHelperChunk(chunk string) string {
	return strings.NewReplacer(`\r`, "\r", `\n`, "\n", `\t`, "\t").Replace(chunk)
}
