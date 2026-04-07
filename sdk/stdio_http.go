package mesh

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
)

const (
	stdioHTTPDetectTimeout   = 250 * time.Millisecond
	stdioHTTPPeekBytes       = 8
	stdioHTTPChunkBufferSize = 32 * 1024
)

var (
	errSTDIOHTTPNoOutput = errors.New("stdio http backend produced no output")

	stdioHTTPMethodPrefixes = []string{
		"GET ",
		"POST ",
		"PUT ",
		"HEAD",
		"OPTI",
		"PATC",
		"DELE",
		"TRAC",
		"CONN",
	}
)

func (r *routeState) handleSTDIOClient(clientConn net.Conn) {
	switch r.cfg.StdioMode {
	case stdioModeHTTP:
		r.handleSTDIOHTTPRequest(clientConn, bufio.NewReader(clientConn))
	case stdioModeAuto:
		reader, isHTTP := detectSTDIOHTTP(clientConn)
		if isHTTP {
			r.handleSTDIOHTTPRequest(clientConn, reader)
			return
		}
		r.handleSTDIOStreamConnection(clientConn, reader)
	default:
		r.handleSTDIOStreamConnection(clientConn, clientConn)
	}
}

func detectSTDIOHTTP(clientConn net.Conn) (*bufio.Reader, bool) {
	reader := bufio.NewReader(clientConn)

	_ = clientConn.SetReadDeadline(time.Now().Add(stdioHTTPDetectTimeout))
	peeked, err := reader.Peek(stdioHTTPPeekBytes)
	_ = clientConn.SetReadDeadline(time.Time{})

	if err != nil {
		var netErr net.Error
		if errors.As(err, &netErr) && netErr.Timeout() && len(peeked) == 0 {
			return reader, false
		}
		if errors.Is(err, io.EOF) && len(peeked) == 0 {
			return reader, false
		}
	}

	if len(peeked) < 4 {
		return reader, false
	}

	snippet := strings.ToUpper(string(peeked))
	for _, prefix := range stdioHTTPMethodPrefixes {
		if strings.HasPrefix(snippet, prefix) {
			return reader, true
		}
	}

	return reader, false
}

func (r *routeState) handleSTDIOHTTPRequest(clientConn net.Conn, reader *bufio.Reader) {
	req, err := http.ReadRequest(reader)
	if err != nil {
		log.Printf("[L4 STDIO] invalid HTTP request on %s: %v", r.publicPort, err)
		writeRawHTTPError(clientConn, http.StatusBadRequest, "invalid HTTP request")
		return
	}
	defer req.Body.Close()

	proc, backend, err := r.spawnSTDIOBackend(clientConn.RemoteAddr().String(), buildSTDIOHTTPEnv(req, clientConn.RemoteAddr().String()))
	if err != nil {
		log.Printf("[L4 STDIO] HTTP backend spawn failed [%s]: %v", r.cfg.Name, err)
		writeRawHTTPError(clientConn, http.StatusServiceUnavailable, "stdio backend spawn failed")
		return
	}

	backend.acquire()
	defer backend.release()

	log.Printf("[L4 STDIO] HTTP %s %s -> backend %s (PID: %d)", req.Method, req.URL.RequestURI(), backend.cfg.Name, proc.cmd.Process.Pid)

	var responseCommitted atomic.Bool
	requestWriteErrs := make(chan error, 1)
	go func() {
		err := writeHTTPRequestToSTDIO(req, proc.stdin)
		if err != nil && !responseCommitted.Load() {
			_ = killManagedCmd(proc.cmd)
		}
		requestWriteErrs <- err
	}()

	headersCommitted, streamErr := writeChunkedHTTPResponse(clientConn, proc, &responseCommitted)
	if streamErr != nil && !errors.Is(streamErr, errSTDIOHTTPNoOutput) {
		_ = killManagedCmd(proc.cmd)
	}

	_ = proc.stdin.Close()
	requestWriteErr := <-requestWriteErrs
	exitErr := waitForSTDIOExit(proc.cmd)

	if requestWriteErr != nil {
		if !headersCommitted {
			log.Printf("[L4 STDIO] HTTP request forward failed [%s -> %s]: %v", r.publicPort, backend.cfg.Name, requestWriteErr)
			writeRawHTTPError(clientConn, http.StatusBadGateway, "forward request to stdio backend failed")
			return
		}
		log.Printf("[L4 STDIO] HTTP request body forwarding ended after response commit [%s -> %s]: %v", r.publicPort, backend.cfg.Name, requestWriteErr)
	}

	if errors.Is(streamErr, errSTDIOHTTPNoOutput) {
		if exitErr != nil {
			log.Printf("[L4 STDIO] HTTP child process %s (PID: %d) exited before producing a response: %v", backend.cfg.Name, proc.cmd.Process.Pid, exitErr)
			if !headersCommitted {
				writeRawHTTPError(clientConn, http.StatusBadGateway, "stdio backend exited before producing a response")
			}
			return
		}

		if !headersCommitted {
			if err := writeEmptyChunkedHTTPResponse(clientConn); err != nil {
				log.Printf("[L4 STDIO] HTTP empty response write failed [%s -> %s]: %v", r.publicPort, backend.cfg.Name, err)
			}
		}
		return
	}

	if streamErr != nil {
		log.Printf("[L4 STDIO] HTTP response stream failed [%s -> %s]: %v", r.publicPort, backend.cfg.Name, streamErr)
		if !headersCommitted {
			writeRawHTTPError(clientConn, http.StatusBadGateway, "stream response from stdio backend failed")
		}
		return
	}

	if exitErr != nil {
		log.Printf("[L4 STDIO] HTTP child process %s (PID: %d) exited with error: %v", backend.cfg.Name, proc.cmd.Process.Pid, exitErr)
	}
}

func writeHTTPRequestToSTDIO(req *http.Request, stdin io.WriteCloser) error {
	defer stdin.Close()
	return req.Write(stdin)
}

func writeChunkedHTTPResponse(clientConn net.Conn, proc *stdioProcess, responseCommitted *atomic.Bool) (bool, error) {
	writer := bufio.NewWriter(clientConn)
	buf := make([]byte, stdioHTTPChunkBufferSize)
	headersCommitted := false

	for {
		n, err := proc.stdout.Read(buf)
		if n > 0 {
			if proc.logBuf != nil {
				_, _ = proc.logBuf.Write(buf[:n])
			}
			if !headersCommitted {
				if err := writeChunkedHTTPHeaders(writer); err != nil {
					return false, err
				}
				headersCommitted = true
				if responseCommitted != nil {
					responseCommitted.Store(true)
				}
			}
			if err := writeChunkedHTTPChunk(writer, buf[:n]); err != nil {
				return headersCommitted, err
			}
		}

		if err == nil {
			continue
		}
		if errors.Is(err, io.EOF) {
			if !headersCommitted {
				return false, errSTDIOHTTPNoOutput
			}
			if _, writeErr := writer.WriteString("0\r\n\r\n"); writeErr != nil {
				return true, writeErr
			}
			return true, writer.Flush()
		}
		return headersCommitted, err
	}
}

func writeChunkedHTTPHeaders(writer *bufio.Writer) error {
	if _, err := fmt.Fprintf(writer,
		"HTTP/1.1 200 OK\r\n"+
			"Content-Type: text/plain; charset=utf-8\r\n"+
			"Cache-Control: no-cache\r\n"+
			"X-Accel-Buffering: no\r\n"+
			"Connection: close\r\n"+
			"Transfer-Encoding: chunked\r\n\r\n"); err != nil {
		return err
	}
	return writer.Flush()
}

func writeChunkedHTTPChunk(writer *bufio.Writer, chunk []byte) error {
	if _, err := fmt.Fprintf(writer, "%x\r\n", len(chunk)); err != nil {
		return err
	}
	if _, err := writer.Write(chunk); err != nil {
		return err
	}
	if _, err := writer.WriteString("\r\n"); err != nil {
		return err
	}
	return writer.Flush()
}

func writeEmptyChunkedHTTPResponse(clientConn net.Conn) error {
	writer := bufio.NewWriter(clientConn)
	if err := writeChunkedHTTPHeaders(writer); err != nil {
		return err
	}
	if _, err := writer.WriteString("0\r\n\r\n"); err != nil {
		return err
	}
	return writer.Flush()
}

func writeRawHTTPError(clientConn net.Conn, statusCode int, message string) {
	statusText := http.StatusText(statusCode)
	body := message + "\n"
	_, _ = fmt.Fprintf(clientConn,
		"HTTP/1.1 %d %s\r\nContent-Type: text/plain; charset=utf-8\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s",
		statusCode, statusText, len(body), body,
	)
}

func buildSTDIOHTTPEnv(req *http.Request, remoteAddr string) []string {
	remoteHost := remoteAddr
	if host, _, err := net.SplitHostPort(remoteAddr); err == nil && host != "" {
		remoteHost = host
	}

	env := []string{
		"GOPHERMESH_STDIO_MODE=http",
		"GATEWAY_INTERFACE=CGI/1.1",
		"REQUEST_METHOD=" + req.Method,
		"REQUEST_URI=" + req.URL.RequestURI(),
		"PATH_INFO=" + req.URL.Path,
		"QUERY_STRING=" + req.URL.RawQuery,
		"SERVER_PROTOCOL=" + req.Proto,
		"REMOTE_ADDR=" + remoteHost,
		"HTTP_HOST=" + req.Host,
	}

	if req.ContentLength >= 0 {
		env = append(env, "CONTENT_LENGTH="+strconv.FormatInt(req.ContentLength, 10))
	}
	if contentType := req.Header.Get("Content-Type"); contentType != "" {
		env = append(env, "CONTENT_TYPE="+contentType)
	}

	for key, values := range req.Header {
		if len(values) == 0 {
			continue
		}
		envKey := "HTTP_" + sanitizeSTDIOHTTPEnvKey(key)
		env = append(env, envKey+"="+strings.Join(values, ", "))
	}

	return env
}

func sanitizeSTDIOHTTPEnvKey(key string) string {
	key = strings.ToUpper(strings.TrimSpace(key))
	replacer := strings.NewReplacer("-", "_", " ", "_", ":", "_")
	key = replacer.Replace(key)

	var builder strings.Builder
	builder.Grow(len(key))
	for _, r := range key {
		switch {
		case r >= 'A' && r <= 'Z':
			builder.WriteRune(r)
		case r >= '0' && r <= '9':
			builder.WriteRune(r)
		case r == '_':
			builder.WriteRune(r)
		}
	}

	return builder.String()
}
