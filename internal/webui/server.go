// Package webui exposes the shared supervisor through a loopback-only graphical UI.
package webui

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/and1truong/heron/internal/config"
	"github.com/and1truong/heron/internal/observe"
	"github.com/and1truong/heron/internal/process"
	"github.com/and1truong/heron/internal/supervisor"
	"gopkg.in/yaml.v3"
)

//go:embed static/*
var assets embed.FS

type Controller interface {
	Snapshots() []supervisor.Snapshot
	Action(context.Context, string, string) error
}
type endpointView struct {
	config.RuntimeEndpointConfig
	URL     string
	Address string
}

// Browser links target the proxy, so opening a stopped app retains lazy startup.
func endpointViews(cfg config.RuntimeConfig, app config.RuntimeAppConfig) []endpointView {
	result := make([]endpointView, 0)
	for _, endpoint := range app.EndpointList() {
		view := endpointView{RuntimeEndpointConfig: endpoint}
		host := endpoint.Host
		if host == "" {
			host = "127.0.0.1"
		}
		switch endpoint.Protocol {
		case config.ProtocolHTTP:
			path := endpoint.Path
			if path == "" {
				path = "/"
			}
			target := url.URL{Scheme: "http", Host: net.JoinHostPort(host, fmt.Sprint(cfg.Port)), Path: path}
			view.URL = target.String()
			view.Address = view.URL
		case config.ProtocolGRPC:
			view.Address = "grpc://" + net.JoinHostPort(host, fmt.Sprint(cfg.Port))
		case config.ProtocolTCP:
			view.Address = "tcp://" + net.JoinHostPort("127.0.0.1", fmt.Sprint(endpoint.ListenPort))
		}
		result = append(result, view)
	}
	return result
}

type appView struct {
	supervisor.Snapshot
	Status    string
	Endpoints []endpointView
	CPU       *float64
	RSS       *uint64
	External  bool
	Pwd       string
	Idle      string
}

const Hostname = "ui.heron.localhost"

type Server struct {
	cfg               config.RuntimeConfig
	path, token, host string
	sup               Controller
	store             *observe.Store
	mu                sync.Mutex
	apps              []appView
	busy              map[string]bool
	configMu          sync.Mutex
}

// New creates the UI handler served from Heron's shared HTTP listener.
func New(cfg config.RuntimeConfig, path string, sup Controller, store *observe.Store) (*Server, error) {
	for id, app := range cfg.Apps {
		for _, endpoint := range app.EndpointList() {
			for _, host := range append([]string{endpoint.Host}, endpoint.Aliases...) {
				if strings.EqualFold(host, Hostname) {
					return nil, fmt.Errorf("app %q endpoint %q uses %s, which is reserved by heron ui", id, endpoint.Name, Hostname)
				}
			}
		}
	}
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		return nil, err
	}
	return &Server{
		cfg: cfg, path: path, token: hex.EncodeToString(token),
		host: net.JoinHostPort(Hostname, strconv.Itoa(cfg.Port)),
		sup:  sup, store: store, busy: map[string]bool{},
	}, nil
}

// URL is the stable browser address for the graphical UI.
func (s *Server) URL() string { return "http://" + s.host }

// HandlesHost reports whether host addresses the reserved graphical UI route.
func (s *Server) HandlesHost(host string) bool {
	if hostname, _, err := net.SplitHostPort(strings.TrimSpace(host)); err == nil {
		host = hostname
	}
	return strings.EqualFold(strings.Trim(strings.TrimSpace(host), "[]"), Hostname)
}

// Start collects shared supervisor snapshots until ctx is canceled.
func (s *Server) Start(ctx context.Context) func() {
	workCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		s.collect(workCtx)
	}()
	return func() {
		cancel()
		<-done
	}
}

func (s *Server) collect(ctx context.Context) {
	var sampler process.Sampler
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	ready := false
	for {
		before := s.sup.Snapshots()
		sampleCtx, cancel := context.WithTimeout(ctx, 750*time.Millisecond)
		stats, err := (process.OSTracker{}).Snapshot(sampleCtx)
		cancel()
		stats = sampler.Sample(time.Now(), stats)
		apps := make([]appView, 0, len(before))
		for _, snap := range s.sup.Snapshots() {
			cfg := s.cfg.Apps[snap.ID]
			a := appView{Snapshot: snap, Status: snap.State.String(), Endpoints: endpointViews(s.cfg, cfg), External: cfg.Stop != "", Pwd: cfg.Pwd, Idle: cfg.Idle.String()}
			stable := false
			for _, b := range before {
				if b.ID == snap.ID && b.PID == snap.PID && b.StartedAt == snap.StartedAt {
					stable = true
				}
			}
			if stable && err == nil && !a.External {
				owned := process.Owned(snap.PID, stats)
				if len(owned) > 0 {
					cpu := 0.0
					rss := uint64(0)
					for _, p := range owned {
						cpu += p.CPU
						rss += p.RSS
					}
					a.RSS = &rss
					if ready {
						a.CPU = &cpu
					}
				}
			}
			apps = append(apps, a)
		}
		ready = err == nil
		s.mu.Lock()
		s.apps = apps
		s.mu.Unlock()
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
	}
}
func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self'; style-src 'self'; img-src 'self'; frame-ancestors 'none'; base-uri 'none'; form-action 'self'")
	if !s.HandlesHost(r.Host) {
		http.Error(w, "invalid host", http.StatusForbidden)
		return
	}
	if origin := r.Header.Get("Origin"); origin != "" && origin != s.URL() {
		http.Error(w, "invalid origin", http.StatusForbidden)
		return
	}
	if strings.HasPrefix(r.URL.Path, "/api/") {
		if r.Header.Get("X-Heron-Token") != s.token {
			http.Error(w, "invalid session", http.StatusForbidden)
			return
		}
		s.api(w, r)
		return
	}
	if r.Method != "GET" {
		http.Error(w, "method not allowed", 405)
		return
	}
	switch r.URL.Path {
	case "/":
		b, _ := assets.ReadFile("static/index.html")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = io.WriteString(w, strings.ReplaceAll(string(b), "SESSION_TOKEN", s.token))
	case "/app.js", "/style.css":
		b, _ := assets.ReadFile("static" + r.URL.Path)
		if strings.HasSuffix(r.URL.Path, ".js") {
			w.Header().Set("Content-Type", "text/javascript")
		} else {
			w.Header().Set("Content-Type", "text/css")
		}
		_, _ = w.Write(b)
	default:
		http.NotFound(w, r)
	}
}
func (s *Server) api(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.URL.Path == "/api/state" && r.Method == "GET":
		s.mu.Lock()
		defer s.mu.Unlock()
		writeJSON(w, map[string]any{"apps": s.apps, "busy": s.busy, "configPath": s.path})
	case r.URL.Path == "/api/logs" && r.Method == "GET":
		id := r.URL.Query().Get("app")
		if _, ok := s.cfg.Apps[id]; !ok && id != "" {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, s.store.Entries(id, r.URL.Query().Get("events") == "true"))
	case r.URL.Path == "/api/action" && r.Method == "POST":
		var req struct{ ID, Action string }
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil {
			http.Error(w, "invalid action", 400)
			return
		}
		if _, ok := s.cfg.Apps[req.ID]; !ok {
			http.NotFound(w, r)
			return
		}
		if req.Action != "start" && req.Action != "stop" && req.Action != "restart" {
			http.Error(w, "invalid action", 400)
			return
		}
		s.mu.Lock()
		if s.busy[req.ID] {
			s.mu.Unlock()
			http.Error(w, "operation already pending", 409)
			return
		}
		s.busy[req.ID] = true
		s.mu.Unlock()
		defer func() { s.mu.Lock(); delete(s.busy, req.ID); s.mu.Unlock() }()
		if err := s.sup.Action(r.Context(), req.ID, req.Action); err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
		writeJSON(w, map[string]bool{"ok": true})
	case r.URL.Path == "/api/config" && (r.Method == "GET" || r.Method == "POST"):
		s.configAPI(w, r)
	default:
		http.NotFound(w, r)
	}
}
func mapping(n *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(n.Content); i += 2 {
		if n.Content[i].Value == key {
			return n.Content[i+1]
		}
	}
	return nil
}

// Single documents only: never silently discard a trailing YAML document.
func decodeDocument(content []byte, node *yaml.Node) error {
	decoder := yaml.NewDecoder(strings.NewReader(string(content)))
	if err := decoder.Decode(node); err != nil {
		return err
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("expected a single YAML document")
	}
	return nil
}
func digest(b []byte) string { v := sha256.Sum256(b); return hex.EncodeToString(v[:]) }
func (s *Server) configAPI(w http.ResponseWriter, r *http.Request) {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	id := r.URL.Query().Get("app")
	path := s.path
	if a, ok := s.cfg.Apps[id]; ok && a.Source != "" {
		path = a.Source
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	path = resolved
	raw, err := os.ReadFile(path)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	var doc yaml.Node
	if err = decodeDocument(raw, &doc); err != nil || len(doc.Content) == 0 {
		http.Error(w, "invalid config document", 400)
		return
	}
	apps := mapping(doc.Content[0], "apps")
	if r.Method == "GET" {
		text := "pwd: /path/to/project\nlaunch: echo hello\n"
		if apps != nil {
			if n := mapping(apps, id); n != nil {
				b, _ := yaml.Marshal(n)
				text = string(b)
			}
		}
		writeJSON(w, map[string]string{"yaml": text, "version": digest(raw), "source": path})
		return
	}
	var req struct {
		YAML, Version string
		Create        bool
	}
	if err = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		http.Error(w, "invalid configuration payload", 400)
		return
	}
	if strings.TrimSpace(id) == "" {
		http.Error(w, "app name is required", 400)
		return
	}
	if req.Version != digest(raw) {
		http.Error(w, "configuration changed; reopen editor before saving", 409)
		return
	}
	if req.Create {
		full, err := config.LoadStrict(s.path)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if _, exists := full.Apps[id]; exists {
			http.Error(w, "app already exists", 409)
			return
		}
	}
	var value yaml.Node
	if err = decodeDocument([]byte(req.YAML), &value); err != nil || len(value.Content) != 1 || value.Content[0].Kind != yaml.MappingNode {
		http.Error(w, "app configuration must be a YAML mapping", 400)
		return
	}
	if apps != nil && apps.Kind == yaml.ScalarNode && apps.Tag == "!!null" {
		apps.Kind = yaml.MappingNode
		apps.Tag = "!!map"
		apps.Value = ""
	}
	if apps == nil {
		apps = &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		doc.Content[0].Content = append(doc.Content[0].Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "apps"}, apps)
	}
	found := false
	for i := 0; i+1 < len(apps.Content); i += 2 {
		if apps.Content[i].Value == id {
			apps.Content[i+1] = value.Content[0]
			found = true
			break
		}
	}
	if !found {
		apps.Content = append(apps.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: id}, value.Content[0])
	}
	updated, err := yaml.Marshal(&doc)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	// Validate the complete resource graph using an overlay, without touching live files.
	if err = config.ValidateOverlay(s.path, path, updated); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".heron-ui-*.yaml")
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err = tmp.Chmod(info.Mode().Perm()); err == nil {
		_, err = tmp.Write(updated)
	}
	if err == nil {
		err = tmp.Sync()
	}
	closeErr := tmp.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		latest, e := os.ReadFile(path)
		if e != nil {
			err = e
		} else if digest(latest) != req.Version {
			http.Error(w, "configuration changed; reopen editor", 409)
			return
		}
	}
	if err == nil {
		err = os.Rename(name, path)
	}
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, map[string]string{"message": "Saved. Restart Heron to apply configuration changes."})
}
