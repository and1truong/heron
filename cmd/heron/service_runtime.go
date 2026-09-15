package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/and1truong/heron/internal/webui"
)

func checkUIReady(ctx context.Context, port int) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("http://127.0.0.1:%d/", port), nil)
	if err != nil {
		return err
	}
	req.Host = fmt.Sprintf("%s:%d", webui.Hostname, port)
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("UI readiness: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("UI readiness: HTTP %d", resp.StatusCode)
	}
	return nil
}

// Background graphical mode needs both the UI's in-memory observations and
// persistent service diagnostics, including managed-app stdout/stderr.
type dualHandler struct{ first, second slog.Handler }

func (h dualHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.first.Enabled(ctx, level) || h.second.Enabled(ctx, level)
}
func (h dualHandler) Handle(ctx context.Context, record slog.Record) error {
	var a, b error
	if h.first.Enabled(ctx, record.Level) {
		a = h.first.Handle(ctx, record.Clone())
	}
	if h.second.Enabled(ctx, record.Level) {
		b = h.second.Handle(ctx, record.Clone())
	}
	return errors.Join(a, b)
}
func (h dualHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return dualHandler{h.first.WithAttrs(attrs), h.second.WithAttrs(attrs)}
}
func (h dualHandler) WithGroup(name string) slog.Handler {
	return dualHandler{h.first.WithGroup(name), h.second.WithGroup(name)}
}
