// Package control exposes Heron's loopback-only process control API.
package control

import (
	"net"
	"net/http"
	"strings"
	"sync"
)

const (
	Hostname      = "heron.localhost"
	RestartPath   = "/_heron/restart"
	commandHeader = "X-Heron-Command"
)

type Handler struct {
	restart func()
	once    sync.Once
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
	if r.URL.Path != RestartPath {
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
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write([]byte("{\"restarting\":true}\n"))
	h.once.Do(h.restart)
}
