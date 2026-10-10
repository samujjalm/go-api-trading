package http

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strings"
	"time"
)

type ctxKey int

const (
	ctxLogger ctxKey = iota
	ctxUserID
)

func logger(ctx context.Context, fallback *slog.Logger) *slog.Logger {
	if l, ok := ctx.Value(ctxLogger).(*slog.Logger); ok {
		return l
	}
	return fallback
}

func userID(ctx context.Context) string {
	id, _ := ctx.Value(ctxUserID).(string)
	return id
}

// withRequestID tags every request with an ID (reusing the caller's
// X-Request-ID if present) and stores a logger carrying it, so every log
// line for one request can be correlated across services.
func (s *Server) withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if id == "" || len(id) > 64 {
			b := make([]byte, 8)
			rand.Read(b)
			id = hex.EncodeToString(b)
		}
		w.Header().Set("X-Request-ID", id)
		ctx := context.WithValue(r.Context(), ctxLogger, s.log.With("request_id", id))
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (s *Server) withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		logger(r.Context(), s.log).Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration_ms", time.Since(start).Milliseconds(),
		)
	})
}

func (s *Server) withRecover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if v := recover(); v != nil {
				if v == http.ErrAbortHandler {
					panic(v)
				}
				logger(r.Context(), s.log).Error("panic", "value", v, "stack", string(debug.Stack()))
				writeError(w, http.StatusInternalServerError, "internal", "internal server error")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) requireAuth(next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || token == "" {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeError(w, http.StatusUnauthorized, "unauthorized", "missing bearer token")
			return
		}
		id, err := s.auth.Authenticate(r.Context(), token)
		if err != nil {
			w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
			writeError(w, http.StatusUnauthorized, "unauthorized", "invalid or expired token")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), ctxUserID, id)))
	})
}
