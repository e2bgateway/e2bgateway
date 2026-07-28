// Package middleware provides HTTP middleware for the E2BGateway server.
package middleware

import (
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/e2bgateway/e2bgateway/internal/auth"
	"github.com/e2bgateway/e2bgateway/internal/config"
)

// Health check endpoint paths.
const (
	healthzPath  = "/healthz"
	readyzPath   = "/readyz"
	metricsPath  = "/metrics"
)

// Logger is the package-level logger.
var logger *zap.Logger

func init() {
	logger, _ = zap.NewProduction()
}

// RealIP extracts the real client IP from proxy headers.
func RealIP(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			parts := strings.SplitN(xff, ",", 2)
			if ip := strings.TrimSpace(parts[0]); ip != "" {
				r.RemoteAddr = ip + ":0"
			}
		} else if xri := r.Header.Get("X-Real-Ip"); xri != "" {
			r.RemoteAddr = xri + ":0"
		}
		next.ServeHTTP(w, r)
	})
}

// RequestLogger logs each HTTP request with timing information.
func RequestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		ww := &statusWriter{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(ww, r)

		duration := time.Since(start)
		ip, _, _ := net.SplitHostPort(r.RemoteAddr)

		logger.Info("http request",
			zap.String("method", r.Method),
			zap.String("path", r.URL.Path),
			zap.Int("status", ww.status),
			zap.Duration("duration", duration),
			zap.String("ip", ip),
			zap.String("request_id", r.Header.Get("X-Request-Id")),
			zap.String("user_agent", r.UserAgent()),
		)
	})
}

// Recovery recovers from panics and returns 500.
func Recovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if err := recover(); err != nil {
				logger.Error("panic recovered",
					zap.Any("error", err),
					zap.String("path", r.URL.Path),
				)
				http.Error(w, `{"code":500,"message":"Internal Server Error"}`, http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// CORS sets CORS headers.
func CORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Accept, Content-Type, Authorization, X-API-Key")
		w.Header().Set("Access-Control-Max-Age", "86400")

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Auth creates an authentication middleware.
func Auth(mgr *auth.Manager) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Skip auth for health endpoints
			switch r.URL.Path {
			case healthzPath, readyzPath, metricsPath:
				next.ServeHTTP(w, r)
				return
			}

			if mgr == nil {
				next.ServeHTTP(w, r)
				return
			}

			tc, err := mgr.Authenticate(r)
			if err != nil {
				WriteError(w, http.StatusUnauthorized, "Unauthorized", err.Error())
				return
			}

			// Add tenant context to request
			r = auth.ContextWithTenant(r, tc)
			next.ServeHTTP(w, r)
		})
	}
}

// RateLimit creates a rate limiting middleware.
func RateLimit(cfg config.RateLimitConfig) func(http.Handler) http.Handler {
	limiter := auth.NewRateLimiter(cfg)

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Skip rate limiting for health endpoints
			if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" || r.URL.Path == "/metrics" {
				next.ServeHTTP(w, r)
				return
			}

			// Use tenant ID as rate limit key if available, otherwise use IP
			key := ""
			if tc, ok := auth.TenantFromContext(r.Context()); ok {
				key = tc.TenantID
			} else {
				ip, _, _ := net.SplitHostPort(r.RemoteAddr)
				key = ip
			}

			if !limiter.Allow(key) {
				w.Header().Set("Retry-After", "60")
				WriteError(w, http.StatusTooManyRequests, "RateLimitExceeded", "Rate limit exceeded. Please retry after some time.")
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

// AuditLog logs audit events for API operations.
func AuditLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		// Skip audit logging for health endpoints
		if r.URL.Path == "/healthz" || r.URL.Path == "/readyz" || r.URL.Path == "/metrics" {
			next.ServeHTTP(w, r)
			return
		}

		ww := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(ww, r)

		// Extract tenant info
		tenantID := "anonymous"
		if tc, ok := auth.TenantFromContext(r.Context()); ok {
			tenantID = tc.TenantID
		}

		duration := time.Since(start)
		ip, _, _ := net.SplitHostPort(r.RemoteAddr)

		// Only log mutating operations
		if r.Method == http.MethodPost || r.Method == http.MethodPut ||
			r.Method == http.MethodPatch || r.Method == http.MethodDelete {
			logger.Info("audit",
				zap.String("tenant", tenantID),
				zap.String("method", r.Method),
				zap.String("path", r.URL.Path),
				zap.Int("status", ww.status),
				zap.Duration("duration", duration),
				zap.String("ip", ip),
				zap.String("request_id", r.Header.Get("X-Request-Id")),
			)
		}
	})
}

// statusWriter wraps ResponseWriter to capture the status code.
type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// WriteJSON writes a JSON response with the given status code.
func WriteJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

// WriteError writes a JSON error response.
func WriteError(w http.ResponseWriter, status int, code string, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]interface{}{
		"error": map[string]interface{}{
			"code":    status,
			"message": message,
			"type":    code,
		},
	})
}
