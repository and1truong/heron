// Package control exposes Heron's loopback-only process control API.
package control

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"sync"

	"github.com/and1truong/heron/internal/supervisor"
)

const (
	Hostname      = "heron.localhost"
	RestartPath   = "/_heron/restart"
	commandHeader = "X-Heron-Command"
)

type Handler struct {
	RestartService func(context.Context, string) error
	restart        func()
	once           sync.Once
}

func New(restart func()) *Handler { return &Handler{restart: restart} }

func HandlesHost(host string) bool {
	if hostname, _, err := net.SplitHostPort(strings.TrimSpace(host)); err == nil {
		host = hostname
	}
	return strings.EqualFold(strings.Trim(strings.TrimSpace(host), "[]"), Hostname)
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if !HandlesHost(r.Host) {
		http.Error(w, "invalid host", http.StatusForbidden)
		return
	}
	serviceID := ""
	if strings.HasPrefix(r.URL.Path, "/_heron/services/") && strings.HasSuffix(r.URL.Path, "/restart") {
		serviceID = strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/_heron/services/"), "/restart")
	}
	if r.URL.Path != RestartPath && (serviceID == "" || strings.Contains(serviceID, "/") || h.RestartService == nil) {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if r.Header.Get(commandHeader) != "restart" {
		http.Error(w, "invalid command", http.StatusForbidden)
		return
	}
	if serviceID != "" {
		if err := h.RestartService(r.Context(), serviceID); err != nil {
			status := http.StatusConflict
			if errors.Is(err, supervisor.ErrUnknownService) {
				status = http.StatusNotFound
			}
			http.Error(w, err.Error(), status)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"service": serviceID, "restarted": true})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write([]byte("{\"restarting\":true}\n"))
	h.once.Do(h.restart)
}
