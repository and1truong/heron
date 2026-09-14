package webui

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/and1truong/heron/internal/config"
	"github.com/and1truong/heron/internal/observe"
	"github.com/and1truong/heron/internal/supervisor"
)

type fakeController struct {
	calls int
	err   error
}

func (f *fakeController) Snapshots() []supervisor.Snapshot             { return nil }
func (f *fakeController) Action(context.Context, string, string) error { f.calls++; return f.err }
func request(s *Server, method, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://"+s.host+path, strings.NewReader(body))
	r.Header.Set("X-Heron-Token", s.token)
	w := httptest.NewRecorder()
	s.ServeHTTP(w, r)
	return w
}
func testServer(t *testing.T) *Server {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "heron.yaml")
	if err := os.WriteFile(path, []byte("port: 3100\nidle: 5m\napps:\n  api:\n    pwd: /tmp\n    launch: echo hello\n    env:\n      TOKEN: secret\n  worker:\n    pwd: /tmp\n    launch: echo worker\n"), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadStrict(path)
	if err != nil {
		t.Fatal(err)
	}
	return &Server{cfg: cfg, path: path, token: "test-session", host: "127.0.0.1:4321", sup: &fakeController{}, store: observe.New(), busy: map[string]bool{}}
}
func TestSessionAndHostProtection(t *testing.T) {
	s := testServer(t)
	for _, tc := range []struct{ host, origin, token string }{{"evil.test:4321", "", s.token}, {s.host, "https://evil.test", s.token}, {s.host, "", ""}} {
		r := httptest.NewRequest("POST", "http://"+tc.host+"/api/action", strings.NewReader(`{"ID":"api","Action":"start"}`))
		r.Header.Set("Origin", tc.origin)
		r.Header.Set("X-Heron-Token", tc.token)
		w := httptest.NewRecorder()
		s.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatalf("got %d", w.Code)
		}
	}
	if s.sup.(*fakeController).calls != 0 {
		t.Fatal("unauthorized action executed")
	}
}
func TestActionUsesSupervisorAndRejectsBusy(t *testing.T) {
	s := testServer(t)
	fake := s.sup.(*fakeController)
	fake.err = errors.New("required by web")
	w := request(s, "POST", "/api/action", `{"ID":"api","Action":"restart"}`)
	if w.Code != 409 || !strings.Contains(w.Body.String(), "required by web") || fake.calls != 1 {
		t.Fatal(w.Body.String())
	}
	if s.busy["api"] {
		t.Fatal("pending action leaked")
	}
	s.busy["api"] = true
	request(s, "POST", "/api/action", `{"ID":"api","Action":"start"}`)
	if fake.calls != 1 {
		t.Fatal("duplicate action executed")
	}
	w = request(s, "POST", "/api/action", `{"ID":"api","Action":"kill"}`)
	if w.Code != 400 {
		t.Fatal("unexpected action accepted")
	}
}
func TestConfigurationPreservesAndValidates(t *testing.T) {
	s := testServer(t)
	original, _ := os.ReadFile(s.path)
	payload := func(yaml, version string, create bool) string {
		b, _ := json.Marshal(map[string]any{"YAML": yaml, "Version": version, "Create": create})
		return string(b)
	}
	for _, bad := range []string{"pwd: /tmp\nlaunch: echo ok\n---\nlaunch: ignored\n", "pwd: /tmp\nlaunch: echo changed\ndependsOn: [missing]\n", "pwd: /tmp\nlaunch: echo changed\nunknownField: true\n"} {
		w := request(s, "POST", "/api/config?app=api", payload(bad, digest(original), false))
		if w.Code != 400 {
			t.Fatal(w.Code, w.Body.String())
		}
		now, _ := os.ReadFile(s.path)
		if string(now) != string(original) {
			t.Fatal("invalid edit wrote file")
		}
	}
	w := request(s, "POST", "/api/config?app=api", payload("pwd: /tmp\nlaunch: echo updated\n", digest(original), true))
	if w.Code != 409 {
		t.Fatal("add overwrote existing app")
	}
	w = request(s, "POST", "/api/config?app=api", payload("pwd: /tmp\nlaunch: echo updated\n", digest(original), false))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	cfg, err := config.LoadStrict(s.path)
	if err != nil || cfg.Apps["worker"].Launch != "echo worker" || cfg.Port != 3100 || cfg.Apps["api"].Launch != "echo updated" {
		t.Fatal("unrelated settings lost", err)
	}
	w = request(s, "POST", "/api/config?app=api", payload("pwd: /tmp\nlaunch: echo stale\n", digest(original), false))
	if w.Code != 409 {
		t.Fatal("stale save accepted")
	}
	info, _ := os.Stat(s.path)
	if info.Mode().Perm() != 0600 {
		t.Fatal("permissions changed")
	}
}
func TestResourceEditValidatesWholeGraph(t *testing.T) {
	s := testServer(t)
	source := filepath.Join(filepath.Dir(s.path), "apps.yaml")
	os.WriteFile(source, []byte("apps:\n  db:\n    pwd: /tmp\n    launch: echo db\n"), 0600)
	os.WriteFile(s.path, []byte("resources: [apps.yaml]\napps:\n  api:\n    pwd: /tmp\n    launch: echo api\n    dependsOn: [db]\n"), 0600)
	var err error
	s.cfg, err = config.LoadStrict(s.path)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(source)
	b, _ := json.Marshal(map[string]string{"YAML": "pwd: /tmp\nlaunch: echo db\ndependsOn: [api]\n", "Version": digest(raw)})
	w := request(s, "POST", "/api/config?app=db", string(b))
	if w.Code != 400 || !strings.Contains(w.Body.String(), "cycl") {
		t.Fatal(w.Code, w.Body.String())
	}
	now, _ := os.ReadFile(source)
	if string(now) != string(raw) {
		t.Fatal("cyclic resource written")
	}
}
func TestAssetsAndLogIsolation(t *testing.T) {
	s := testServer(t)
	w := request(s, "GET", "/", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), s.token) || strings.Contains(w.Body.String(), "SESSION_TOKEN") {
		t.Fatal("session bootstrap failed")
	}
	w = request(s, "GET", "/api/state", "")
	if strings.Contains(w.Body.String(), "secret") {
		t.Fatal("environment leaked in state")
	}
	w = request(s, "GET", "/api/logs?app=unknown", "")
	if w.Code != 404 {
		t.Fatal("unknown log scope accepted")
	}
}

func TestEndpointLinksUsePublicRoutes(t *testing.T) {
	cfg := config.RuntimeConfig{Port: 3100}
	app := config.RuntimeAppConfig{Endpoints: map[string]config.RuntimeEndpointConfig{
		"web":     {Name: "web", Protocol: config.ProtocolHTTP, Host: "app.localhost", Port: 8080, Primary: true},
		"metrics": {Name: "metrics", Protocol: config.ProtocolHTTP, Host: "metrics.app.localhost", Port: 9090},
		"admin":   {Name: "admin", Protocol: config.ProtocolHTTP, Path: "/admin console/#status", Port: 8081},
		"rpc":     {Name: "rpc", Protocol: config.ProtocolGRPC, Host: "rpc.app.localhost", Port: 50051},
		"db":      {Name: "db", Protocol: config.ProtocolTCP, Port: 5432, ListenPort: 15432},
	}}
	views := endpointViews(cfg, app)
	want := map[string]string{"web": "http://app.localhost:3100/", "metrics": "http://metrics.app.localhost:3100/", "admin": "http://127.0.0.1:3100/admin%20console/%23status", "rpc": "grpc://rpc.app.localhost:3100", "db": "tcp://127.0.0.1:15432"}
	if len(views) != len(want) {
		t.Fatalf("lost endpoints: %v", views)
	}
	for _, e := range views {
		if e.Address != want[e.Name] {
			t.Errorf("%s address = %s", e.Name, e.Address)
		}
		if e.Protocol != config.ProtocolHTTP && e.URL != "" {
			t.Errorf("non-browser endpoint linked: %s", e.URL)
		}
		if e.Protocol == config.ProtocolHTTP && e.URL != e.Address {
			t.Errorf("missing link: %s", e.Name)
		}
		if e.Name == "web" && !e.Primary {
			t.Error("primary metadata lost")
		}
	}
	legacy := endpointViews(cfg, config.RuntimeAppConfig{Port: 8080, Protocol: config.ProtocolHTTP, Host: "legacy.localhost"})
	if len(legacy) != 1 || legacy[0].URL != "http://legacy.localhost:3100/" {
		t.Fatalf("legacy endpoint: %v", legacy)
	}
	if len(endpointViews(cfg, config.RuntimeAppConfig{})) != 0 {
		t.Fatal("invented endpoint for process-only app")
	}
}
