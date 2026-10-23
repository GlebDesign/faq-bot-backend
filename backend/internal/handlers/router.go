package handlers

import (
	"context"
	"crypto/rand"
	"net/http"
	"regexp"
	"sync/atomic"
	"time"
)

type Health struct {
	Check   func(context.Context) error
	started atomic.Bool
	ready   atomic.Bool
}

func (h *Health) Start() { h.started.Store(true); h.ready.Store(true) }
func (h *Health) Drain() { h.ready.Store(false) }

var requestIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{1,80}$`)

func NewHandler(health *Health) http.Handler {
	mux := http.NewServeMux()
	for _, path := range []string{"/health/startup", "/health/ready", "/health/live"} {
		mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet && r.Method != http.MethodHead {
				w.Header().Set("Allow", "GET, HEAD")
				writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Метод не поддерживается")
				return
			}
			if r.URL.Path == "/health/startup" && !health.started.Load() {
				writeError(w, http.StatusServiceUnavailable, "service_starting", "Сервис запускается")
				return
			}
			if r.URL.Path == "/health/ready" && !health.ready.Load() {
				writeError(w, http.StatusServiceUnavailable, "service_not_ready", "Сервис не готов принимать запросы")
				return
			}
			if r.URL.Path == "/health/ready" && health.Check != nil {
				ctx, cancel := context.WithTimeout(r.Context(), time.Second)
				defer cancel()
				if err := health.Check(ctx); err != nil {
					writeError(w, http.StatusServiceUnavailable, "service_not_ready", "Сервис не готов принимать запросы")
					return
				}
			}
			writeJSON(w, http.StatusOK, struct {
				Status string `json:"status"`
			}{Status: "ok"})
		})
	}
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "Ресурс не найден")
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Request-ID")
		if !requestIDPattern.MatchString(id) {
			id = rand.Text()
		}
		w.Header().Set("X-Request-ID", id)
		w.Header().Set("Cache-Control", "no-store")
		mux.ServeHTTP(w, r)
	})
}
