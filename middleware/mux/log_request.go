package gorilla

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"runtime"
	"strings"

	"github.com/garin-allo/log"
)

// SetLogRequest injects a new log.Request into the request context.
// Must be registered before SaveLogRequest.
func SetLogRequest() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			parentCtx := r.Context()
			newCtx := log.NewRequest().SaveToContext(parentCtx)
			next.ServeHTTP(w, r.WithContext(newCtx))
		})
	}
}

// SaveLogRequest captures the request and response bodies, extracts log data,
// and saves the log entry. Must be registered after SetLogRequest.
func SaveLogRequest() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// ── Capture request body ──────────────────────────────────────
			// Read the body so we can log it, then restore it so the
			// actual handler can still read it.
			var reqBody []byte
			if r.Body != nil && !isBlobRequest(r) {
				reqBody, _ = io.ReadAll(r.Body)
				r.Body = io.NopCloser(bytes.NewBuffer(reqBody))
			}

			// ── Capture response body + status ────────────────────────────
			rw := newBodyDumpResponseWriter(w)

			// ── Recover: log the request even if the handler panics ───────
			defer func() {
				rec := recover()

				ctx := r.Context()
				extractRequestData(ctx, r, rw, reqBody, rw.body.Bytes())

				if rec != nil {
					// Re-panic is http.ErrAbortHandler — let net/http handle it.
					if rec == http.ErrAbortHandler {
						log.Context(ctx).Save()
						panic(rec)
					}

					err, ok := rec.(error)
					if !ok {
						err = fmt.Errorf("%v", r)
					}

					stack := make([]byte, 4<<10)
					stack = stack[:runtime.Stack(stack, false)]

					requestLog := log.Context(ctx)
					requestLog.Debugf("[PANIC RECOVER] %v %s\n", err, stack)

					// Write 500 only if nothing has been written yet.
					if !rw.written {
						http.Error(rw, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
					}
				}

				log.Context(ctx).Save()
			}()

			next.ServeHTTP(rw, r)
		})
	}
}

func extractRequestData(
	ctx context.Context,
	r *http.Request,
	rw *bodyDumpResponseWriter,
	req, resp []byte,
) {
	requestLog := log.Context(ctx)

	requestLog.Source = "api"
	requestLog.Method = r.Method
	requestLog.URL = r.Host + r.URL.String()
	requestLog.ReqHeader = headersFromRequest(r)
	requestLog.RespHeader = headersFromResponse(rw)
	requestLog.StatusCode = rw.status

	// Response body
	if requestLog.RespBody == nil {
		if isBlobResponse(rw) {
			requestLog.RespBody = extractResponseFileName(rw)
		} else if err := json.Unmarshal(resp, &requestLog.RespBody); err != nil {
			requestLog.RespBody = string(resp)
		}
	}

	// Request body
	if r.Method == http.MethodGet || r.Method == http.MethodDelete {
		queryArgs := make(map[string][]string)
		for k, v := range r.URL.Query() {
			queryArgs[k] = v
		}
		requestLog.ReqBody = queryArgs
	} else {
		if requestLog.ReqBody == nil {
			if isBlobRequest(r) {
				requestLog.ReqBody = extractRequestFileNames(r)
			} else if err := json.Unmarshal(req, &requestLog.ReqBody); err != nil {
				requestLog.ReqBody = string(req)
			}
		}
	}
}

// ── Header helpers ────────────────────────────────────────────────────────────

func headersFromRequest(r *http.Request) map[string][]string {
	headers := make(map[string][]string, len(r.Header))
	for k, v := range r.Header {
		headers[k] = v
	}
	return headers
}

func headersFromResponse(rw *bodyDumpResponseWriter) map[string][]string {
	headers := make(map[string][]string, len(rw.Header()))
	for k, v := range rw.Header() {
		headers[k] = v
	}
	return headers
}

// ── Blob detection ────────────────────────────────────────────────────────────

func isBlobRequest(r *http.Request) bool {
	ct := r.Header.Get("Content-Type")
	return strings.Contains(strings.ToLower(ct), "multipart/form-data")
}

func isBlobResponse(rw *bodyDumpResponseWriter) bool {
	ct := rw.Header().Get("Content-Type")
	if strings.Contains(strings.ToLower(ct), "application/octet-stream") {
		return true
	}
	return rw.Header().Get("Content-Disposition") != ""
}

func extractRequestFileNames(r *http.Request) map[string][]string {
	files := make(map[string][]string)
	if err := r.ParseMultipartForm(32 << 20); err != nil && err != http.ErrNotMultipart {
		return files
	}
	if r.MultipartForm == nil {
		return files
	}
	for field, fhs := range r.MultipartForm.File {
		for _, fh := range fhs {
			files[field] = append(files[field], fh.Filename)
		}
	}
	return files
}

func extractResponseFileName(rw *bodyDumpResponseWriter) string {
	disposition := rw.Header().Get("Content-Disposition")
	_, params, err := mime.ParseMediaType(disposition)
	if err == nil {
		if filename := params["filename"]; filename != "" {
			return filename
		}
	}
	return "blob"
}

// ── bodyDumpResponseWriter ────────────────────────────────────────────────────
// Wraps http.ResponseWriter to capture the status code and response body
// without consuming them — the real response is still written to the client.

type bodyDumpResponseWriter struct {
	http.ResponseWriter
	status  int
	body    bytes.Buffer
	written bool // true once WriteHeader or Write has been called
}

func newBodyDumpResponseWriter(w http.ResponseWriter) *bodyDumpResponseWriter {
	return &bodyDumpResponseWriter{
		ResponseWriter: w,
		status:         http.StatusOK,
	}
}

func (rw *bodyDumpResponseWriter) WriteHeader(code int) {
	rw.status = code
	rw.written = true
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *bodyDumpResponseWriter) Write(b []byte) (int, error) {
	rw.written = true
	rw.body.Write(b) // capture a copy
	return rw.ResponseWriter.Write(b)
}

// Unwrap lets http.ResponseController reach the underlying writer.
func (rw *bodyDumpResponseWriter) Unwrap() http.ResponseWriter {
	return rw.ResponseWriter
}
