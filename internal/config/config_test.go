package config

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func baseConfig(dir string) Config {
	return Config{Port: 3000, Apps: map[string]AppConfig{"api": {Pwd: dir, Launch: "server", Path: "/service/api/", Port: 1980}}}
}

func TestNormalizeDefaultsAndPath(t *testing.T) {
	cfg, err := baseConfig(t.TempDir()).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Apps["api"].Path != "/service/api" {
		t.Fatalf("path = %q", cfg.Apps["api"].Path)
	}
	if cfg.Apps["api"].Idle != 30*time.Minute || cfg.StartTimeout != 30*time.Second || cfg.StopTimeout != 10*time.Second {
		t.Fatalf("defaults not applied: %#v", cfg)
	}
}

func TestNormalizeCopiesLifecycleHooks(t *testing.T) {
	c := baseConfig(t.TempDir())
	c.StartUp = []string{"prepare"}
	c.TearDown = []string{"cleanup"}

	cfg, err := c.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	c.StartUp[0] = "changed"
	c.TearDown[0] = "changed"

	if cfg.StartUp[0] != "prepare" || cfg.TearDown[0] != "cleanup" {
		t.Fatalf("lifecycle hooks changed after source config mutation: %#v %#v", cfg.StartUp, cfg.TearDown)
	}
}

func TestLoadLifecycleHooks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "heron.yaml")
	contents := fmt.Sprintf("startUp:\n  - prepare one\n  - prepare two\ntearDown:\n  - cleanup\napps:\n  api:\n    pwd: %q\n    launch: server\n    port: 1980\n", dir)
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.StartUp) != 2 || cfg.StartUp[0] != "prepare one" || cfg.StartUp[1] != "prepare two" {
		t.Fatalf("startUp = %#v", cfg.StartUp)
	}
	if len(cfg.TearDown) != 1 || cfg.TearDown[0] != "cleanup" {
		t.Fatalf("tearDown = %#v", cfg.TearDown)
	}
}

func TestNormalizeScheduledTasks(t *testing.T) {
	c := baseConfig(t.TempDir())
	c.ScheduledTasks = map[string]ScheduledTaskConfig{
		"cleanup": {
			Command:    "./cleanup.sh",
			Every:      Duration{Duration: time.Hour, set: true},
			RunOnStart: true,
		},
	}

	cfg, err := c.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	task := cfg.ScheduledTasks["cleanup"]
	if task.Name != "cleanup" || task.Command != "./cleanup.sh" ||
		task.Every != time.Hour || task.Timeout != 5*time.Minute ||
		!task.RunOnStart || task.Overlap != "skip" {
		t.Fatalf("scheduled task defaults = %#v", task)
	}
}

func TestNormalizeRejectsInvalidScheduledTasks(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name string
		task ScheduledTaskConfig
		want string
	}{
		{name: "missing command", task: ScheduledTaskConfig{Every: Duration{Duration: time.Hour}}, want: "command is required"},
		{name: "missing every", task: ScheduledTaskConfig{Command: "cleanup"}, want: "every must be positive"},
		{name: "invalid timeout", task: ScheduledTaskConfig{Command: "cleanup", Every: Duration{Duration: time.Hour}, Timeout: Duration{Duration: -time.Second, set: true}}, want: "timeout must be positive"},
		{name: "unsupported overlap", task: ScheduledTaskConfig{Command: "cleanup", Every: Duration{Duration: time.Hour}, Overlap: "queue"}, want: "only skip is supported"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := baseConfig(dir)
			c.ScheduledTasks = map[string]ScheduledTaskConfig{"cleanup": tt.task}
			_, err := c.Normalize()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestLoadScheduledTasks(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "heron.yaml")
	contents := fmt.Sprintf("scheduledTasks:\n  cleanup:\n    command: ./cleanup.sh\n    every: 1h\n    timeout: 30s\n    runOnStart: true\n    overlap: skip\napps:\n  api:\n    pwd: %q\n    launch: server\n    port: 1980\n", dir)
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadStrict(path)
	if err != nil {
		t.Fatal(err)
	}
	if task := cfg.ScheduledTasks["cleanup"]; task.Every != time.Hour || task.Timeout != 30*time.Second || !task.RunOnStart {
		t.Fatalf("scheduled task = %#v", task)
	}
}

func TestDefaultPathUsesHeronName(t *testing.T) {
	path, err := DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(path) != "heron.yaml" {
		t.Fatalf("default config path = %q", path)
	}
}

func TestNormalizeExpandsHomeRelativePwd(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	c := baseConfig(t.TempDir())
	app := c.Apps["api"]
	app.Pwd = "~/"
	c.Apps["api"] = app
	cfg, err := c.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Apps["api"].Pwd != home {
		t.Fatalf("pwd = %q, want %q", cfg.Apps["api"].Pwd, home)
	}
}

func TestNormalizeRoutingModes(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name     string
		app      AppConfig
		wantErr  string
		wantHost string
	}{
		{"host only", AppConfig{Pwd: dir, Launch: "server", Host: "FOO.LocalHost", Port: 1980}, "", "foo.localhost"},
		{"path only", AppConfig{Pwd: dir, Launch: "server", Path: "/service/foo", Port: 1980}, "", ""},
		{"path and host", AppConfig{Pwd: dir, Launch: "server", Path: "/service/foo", Host: "foo.localhost", Port: 1980}, "exactly one of path or host is required", ""},
		{"neither path nor host", AppConfig{Pwd: dir, Launch: "server", Port: 1980}, "", "api.localhost"},
		{"include prefix with host", AppConfig{Pwd: dir, Launch: "server", Host: "foo.localhost", Port: 1980, IncludePrefix: true}, "includePrefix is not supported", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := Config{Apps: map[string]AppConfig{"api": tt.app}}
			got, err := c.Normalize()
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Apps["api"].Host != tt.wantHost {
				t.Fatalf("host = %q, want %q", got.Apps["api"].Host, tt.wantHost)
			}
		})
	}
}

func TestNormalizeAssignsDistinctDefaultHosts(t *testing.T) {
	dir := t.TempDir()
	c := Config{Apps: map[string]AppConfig{
		"api": {Pwd: dir, Launch: "server", Port: 1980},
		"web": {Pwd: dir, Launch: "server", Port: 1981},
	}}

	got, err := c.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if got.Apps["api"].Host != "api.localhost" {
		t.Fatalf("api host = %q, want %q", got.Apps["api"].Host, "api.localhost")
	}
	if got.Apps["web"].Host != "web.localhost" {
		t.Fatalf("web host = %q, want %q", got.Apps["web"].Host, "web.localhost")
	}
}

func TestNormalizeNamedEndpointsAndHierarchicalHosts(t *testing.T) {
	dir := t.TempDir()
	c := Config{Apps: map[string]AppConfig{
		"foo": {
			Pwd: dir, Launch: "server",
			Endpoints: map[string]EndpointConfig{
				"web":     {Port: 8080, Primary: true},
				"metrics": {Port: 9090},
			},
		},
	}}

	got, err := c.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	app := got.Apps["foo"]
	if app.Port != 8080 || app.Host != "foo.localhost" {
		t.Fatalf("primary compatibility fields = %#v", app)
	}
	web := app.Endpoints["web"]
	if web.Host != "foo.localhost" || len(web.Aliases) != 1 || web.Aliases[0] != "web.foo.localhost" || !web.Primary {
		t.Fatalf("web endpoint = %#v", web)
	}
	if metrics := app.Endpoints["metrics"]; metrics.Host != "metrics.foo.localhost" || metrics.Primary {
		t.Fatalf("metrics endpoint = %#v", metrics)
	}
}

func TestNormalizeSingleNamedEndpointBecomesPrimary(t *testing.T) {
	dir := t.TempDir()
	got, err := (Config{Apps: map[string]AppConfig{
		"foo": {Pwd: dir, Launch: "server", Endpoints: map[string]EndpointConfig{"web": {Port: 8080}}},
	}}).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if endpoint := got.Apps["foo"].Endpoints["web"]; !endpoint.Primary || endpoint.Host != "foo.localhost" {
		t.Fatalf("endpoint = %#v", endpoint)
	}
}

func TestNormalizeAllowsProcessOnlyApp(t *testing.T) {
	dir := t.TempDir()
	got, err := (Config{Apps: map[string]AppConfig{"worker": {Pwd: dir, Launch: "worker"}}}).Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if endpoints := got.Apps["worker"].EndpointList(); len(endpoints) != 0 {
		t.Fatalf("endpoints = %#v, want none", endpoints)
	}
}

func TestNormalizeRejectsInvalidEndpointConfigurations(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name      string
		endpoints map[string]EndpointConfig
		legacy    AppConfig
		want      string
	}{
		{name: "missing primary", endpoints: map[string]EndpointConfig{"web": {Port: 8080}, "metrics": {Port: 9090}}, want: "exactly one primary"},
		{name: "multiple primaries", endpoints: map[string]EndpointConfig{"web": {Port: 8080, Primary: true}, "metrics": {Port: 9090, Primary: true}}, want: "exactly one primary"},
		{name: "invalid name", endpoints: map[string]EndpointConfig{"bad.name": {Port: 8080, Primary: true}}, want: "valid DNS label"},
		{name: "legacy conflict", endpoints: map[string]EndpointConfig{"web": {Port: 8080, Primary: true}}, legacy: AppConfig{Port: 3001}, want: "cannot be combined"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app := tt.legacy
			app.Pwd, app.Launch, app.Endpoints = dir, "server", tt.endpoints
			_, err := (Config{Apps: map[string]AppConfig{"foo": app}}).Normalize()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestNormalizeRejectsGeneratedAliasCollisionCaseInsensitively(t *testing.T) {
	dir := t.TempDir()
	_, err := (Config{Apps: map[string]AppConfig{
		"Foo": {
			Pwd: dir, Launch: "server",
			Endpoints: map[string]EndpointConfig{
				"web":     {Port: 8080, Primary: true},
				"metrics": {Port: 9090, Host: "web.foo.localhost"},
			},
		},
	}}).Normalize()
	if err == nil || !strings.Contains(err.Error(), "duplicate host") {
		t.Fatalf("error = %v, want duplicate host", err)
	}
}

func TestNormalizeRejectsListenPortBackendPortCollision(t *testing.T) {
	dir := t.TempDir()
	_, err := (Config{Apps: map[string]AppConfig{
		"foo": {
			Pwd: dir, Launch: "server",
			Endpoints: map[string]EndpointConfig{
				"web": {Port: 8080, Primary: true},
				"db":  {Port: 5432, Protocol: ProtocolTCP, ListenPort: 8080},
			},
		},
	}}).Normalize()
	if err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Fatalf("error = %v, want listen/backend port conflict", err)
	}
}

func TestNormalizeRejectsBackendPortWrapperPortCollision(t *testing.T) {
	dir := t.TempDir()
	_, err := (Config{Port: 3000, Apps: map[string]AppConfig{
		"foo": {
			Pwd: dir, Launch: "server",
			Endpoints: map[string]EndpointConfig{
				"web": {Port: 3000, Primary: true},
			},
		},
	}}).Normalize()
	if err == nil || !strings.Contains(err.Error(), "conflicts with wrapper port") {
		t.Fatalf("error = %v, want backend/wrapper port conflict", err)
	}
}

func TestNormalizeTCPRouting(t *testing.T) {
	dir := t.TempDir()
	c := Config{Port: 3000, Apps: map[string]AppConfig{
		"db": {Pwd: dir, Launch: "postgres", Protocol: "TCP", ListenPort: 15432, Port: 5432},
	}}
	got, err := c.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if app := got.Apps["db"]; app.Protocol != ProtocolTCP || app.ListenPort != 15432 || app.Port != 5432 {
		t.Fatalf("TCP app = %#v", app)
	}
}

func TestNormalizeGRPCRouting(t *testing.T) {
	dir := t.TempDir()
	c := Config{Apps: map[string]AppConfig{
		"greeter": {
			Pwd: dir, Launch: "server", Protocol: "GRPC", Port: 50051,
			Health: HealthConfig{GRPC: true},
		},
	}}
	got, err := c.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	app := got.Apps["greeter"]
	if app.Protocol != ProtocolGRPC || app.Host != "greeter.localhost" || !app.GRPCHealth {
		t.Fatalf("gRPC app = %#v", app)
	}
}

func TestNormalizeRejectsInvalidGRPCRouting(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name string
		app  AppConfig
		want string
	}{
		{"path", AppConfig{Pwd: dir, Launch: "server", Protocol: "grpc", Path: "/greeter", Port: 50051}, "not supported with gRPC"},
		{"include prefix", AppConfig{Pwd: dir, Launch: "server", Protocol: "grpc", IncludePrefix: true, Port: 50051}, "not supported with gRPC"},
		{"listen port", AppConfig{Pwd: dir, Launch: "server", Protocol: "grpc", ListenPort: 15051, Port: 50051}, "only supported with TCP"},
		{"gRPC health on HTTP", AppConfig{Pwd: dir, Launch: "server", Protocol: "http", Port: 50051, Health: HealthConfig{GRPC: true}}, "only supported with gRPC"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := (Config{Apps: map[string]AppConfig{"greeter": tt.app}}).Normalize()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestNormalizeRejectsInvalidTCPRouting(t *testing.T) {
	dir := t.TempDir()
	tests := []struct {
		name string
		app  AppConfig
		want string
	}{
		{"missing listen port", AppConfig{Pwd: dir, Launch: "server", Protocol: "tcp", Port: 5432}, "invalid listenPort"},
		{"path", AppConfig{Pwd: dir, Launch: "server", Protocol: "tcp", Path: "/db", ListenPort: 15432, Port: 5432}, "not supported with TCP"},
		{"wrapper conflict", AppConfig{Pwd: dir, Launch: "server", Protocol: "tcp", ListenPort: 3000, Port: 5432}, "conflicts with wrapper port"},
		{"unknown protocol", AppConfig{Pwd: dir, Launch: "server", Protocol: "udp", Port: 5432}, "invalid protocol"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := (Config{Port: 3000, Apps: map[string]AppConfig{"db": tt.app}}).Normalize()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestNormalizeRejectsDuplicateTCPListenPorts(t *testing.T) {
	dir := t.TempDir()
	c := Config{Apps: map[string]AppConfig{
		"postgres": {Pwd: dir, Launch: "postgres", Protocol: "tcp", ListenPort: 15432, Port: 5432},
		"redis":    {Pwd: dir, Launch: "redis", Protocol: "tcp", ListenPort: 15432, Port: 6379},
	}}
	if _, err := c.Normalize(); err == nil || !strings.Contains(err.Error(), "duplicate listenPort") {
		t.Fatalf("error = %v, want duplicate listenPort", err)
	}
}

func TestNormalizeRejectsDuplicateHosts(t *testing.T) {
	dir := t.TempDir()
	c := Config{Apps: map[string]AppConfig{
		"foo": {Pwd: dir, Launch: "server", Host: "foo.localhost", Port: 1980},
		"bar": {Pwd: dir, Launch: "server", Host: "FOO.LOCALHOST", Port: 1981},
	}}
	if _, err := c.Normalize(); err == nil || !strings.Contains(err.Error(), "duplicate host") {
		t.Fatalf("error = %v, want duplicate host", err)
	}
}

func TestNormalizeRejectsReservedControlHostname(t *testing.T) {
	dir := t.TempDir()
	c := Config{Apps: map[string]AppConfig{
		"api": {Pwd: dir, Launch: "server", Host: "HERON.LOCALHOST", Port: 1980},
	}}
	if _, err := c.Normalize(); err == nil || !strings.Contains(err.Error(), "reserved by heron control API") {
		t.Fatalf("error = %v, want reserved control host", err)
	}
}

func TestNormalizeIdleOverride(t *testing.T) {
	c := baseConfig(t.TempDir())
	c.Idle = Duration{Duration: time.Hour}
	override := Duration{Duration: time.Minute}
	app := c.Apps["api"]
	app.Idle = &override
	c.Apps["api"] = app
	cfg, err := c.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Apps["api"].Idle != time.Minute {
		t.Fatal("idle override not applied")
	}
}

func TestNormalizeZeroAppIdleDisablesShutdown(t *testing.T) {
	c := baseConfig(t.TempDir())
	zero := Duration{Duration: 0, set: true}
	app := c.Apps["api"]
	app.Idle = &zero
	c.Apps["api"] = app
	cfg, err := c.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Apps["api"].Idle != 0 {
		t.Fatalf("idle = %s, want disabled", cfg.Apps["api"].Idle)
	}
}

func TestLoadAcceptsZeroAppIdle(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "heron.yaml")
	contents := fmt.Sprintf("apps:\n  kafka:\n    pwd: %q\n    launch: kafka\n    protocol: tcp\n    listenPort: 19092\n    port: 9092\n    idle: 0\n", dir)
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Apps["kafka"].Idle != 0 {
		t.Fatalf("idle = %s, want disabled", cfg.Apps["kafka"].Idle)
	}
}

func TestLoadAcceptsLiteralPerAppEnvironment(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "heron.yaml")
	contents := fmt.Sprintf("apps:\n  api:\n    pwd: %q\n    launch: server\n    port: 1980\n    env:\n      APP_ENV: development\n      EMPTY: \"\"\n      LITERAL: ${HOME}\n", dir)
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"APP_ENV": "development", "EMPTY": "", "LITERAL": "${HOME}"}
	if got := cfg.Apps["api"].Env; !maps.Equal(got, want) {
		t.Fatalf("env = %#v, want %#v", got, want)
	}
}

func TestNormalizeCopiesPerAppEnvironment(t *testing.T) {
	c := baseConfig(t.TempDir())
	app := c.Apps["api"]
	app.Env = map[string]string{"APP_ENV": "development"}
	c.Apps["api"] = app

	cfg, err := c.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	app.Env["APP_ENV"] = "production"

	if got := cfg.Apps["api"].Env["APP_ENV"]; got != "development" {
		t.Fatalf("runtime env changed to %q after source config mutation", got)
	}
}

func TestNormalizeRejectsMalformedEnvironment(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{"empty name", map[string]string{"": "value"}, `invalid environment variable name ""`},
		{"equals in name", map[string]string{"BAD=NAME": "value"}, `invalid environment variable name "BAD=NAME"`},
		{"NUL in name", map[string]string{"BAD\x00NAME": "value"}, `invalid environment variable name "BAD\x00NAME"`},
		{"NUL in value", map[string]string{"SECRET": "hidden\x00value"}, `environment variable "SECRET" contains NUL`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := baseConfig(t.TempDir())
			app := c.Apps["api"]
			app.Env = tt.env
			c.Apps["api"] = app

			_, err := c.Normalize()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
			if strings.Contains(err.Error(), "hidden") {
				t.Fatalf("error exposes environment value: %v", err)
			}
		})
	}
}

func TestNormalizeRejectsNegativeAppIdle(t *testing.T) {
	c := baseConfig(t.TempDir())
	negative := Duration{Duration: -time.Second, set: true}
	app := c.Apps["api"]
	app.Idle = &negative
	c.Apps["api"] = app
	if _, err := c.Normalize(); err == nil || !strings.Contains(err.Error(), "idle must be non-negative") {
		t.Fatalf("error = %v, want non-negative idle validation", err)
	}
}

func TestValidation(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{"bad port", func(c *Config) { c.Port = 70000 }, "invalid wrapper port"},
		{"bad service port", func(c *Config) { a := c.Apps["api"]; a.Port = 0; c.Apps["api"] = a }, "invalid port"},
		{"missing launch", func(c *Config) { a := c.Apps["api"]; a.Launch = ""; c.Apps["api"] = a }, "launch is required"},
		{"bad path", func(c *Config) { a := c.Apps["api"]; a.Path = "api"; c.Apps["api"] = a }, "path must start"},
		{"bad pwd", func(c *Config) { a := c.Apps["api"]; a.Pwd = file; c.Apps["api"] = a }, "not a directory"},
		{"duplicate path", func(c *Config) {
			c.Apps["web"] = AppConfig{Pwd: dir, Launch: "server", Path: "/service/api", Port: 1981}
		}, "duplicate path"},
		{"duplicate port", func(c *Config) { c.Apps["web"] = AppConfig{Pwd: dir, Launch: "server", Path: "/web", Port: 1980} }, "duplicate port"},
		{"log level", func(c *Config) { c.LogLevel = "verbose" }, "invalid log level"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := baseConfig(dir)
			tt.mutate(&c)
			_, err := c.Normalize()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestLoadRejectsBadDuration(t *testing.T) {
	p := filepath.Join(t.TempDir(), "config.yaml")
	_ = os.WriteFile(p, []byte("idle: forever\n"), 0600)
	if _, err := Load(p); err == nil {
		t.Fatal("expected duration error")
	}
}

func TestExplicitZeroDurationIsRejected(t *testing.T) {
	zero := Duration{Duration: 0, set: true}
	c := baseConfig(t.TempDir())
	c.Idle = zero
	if _, err := c.Normalize(); err == nil {
		t.Fatal("expected explicit zero duration to be rejected")
	}
}

func TestLoadComposesNestedResourcesRelativeToDeclaringFile(t *testing.T) {
	dir := t.TempDir()
	appsDir := filepath.Join(dir, "apps")
	if err := os.MkdirAll(filepath.Join(appsDir, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	writeConfigFile(t, filepath.Join(dir, "heron.yaml"), "resources:\n  - apps/backend.yaml\n")
	writeConfigFile(t, filepath.Join(appsDir, "backend.yaml"), "resources:\n  - nested/worker.yaml\napps:\n  api:\n    pwd: "+fmt.Sprintf("%q", dir)+"\n    launch: api\n    port: 1980\n")
	writeConfigFile(t, filepath.Join(appsDir, "nested", "worker.yaml"), "apps:\n  worker:\n    pwd: "+fmt.Sprintf("%q", dir)+"\n    launch: worker\n    port: 1981\n")

	cfg, err := Load(filepath.Join(dir, "heron.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Apps) != 2 || cfg.Apps["api"].Launch != "api" || cfg.Apps["worker"].Launch != "worker" {
		t.Fatalf("apps = %#v", cfg.Apps)
	}
	if cfg.Apps["api"].Source != canonicalTestPath(t, filepath.Join(appsDir, "backend.yaml")) {
		t.Fatalf("api source = %q", cfg.Apps["api"].Source)
	}
}

func TestLoadRejectsDuplicateAppsAcrossResources(t *testing.T) {
	dir := t.TempDir()
	writeConfigFile(t, filepath.Join(dir, "heron.yaml"), "resources:\n  - one.yaml\n  - two.yaml\n")
	app := "apps:\n  api:\n    pwd: " + fmt.Sprintf("%q", dir) + "\n    launch: api\n    port: 1980\n"
	writeConfigFile(t, filepath.Join(dir, "one.yaml"), app)
	writeConfigFile(t, filepath.Join(dir, "two.yaml"), app)

	_, err := Load(filepath.Join(dir, "heron.yaml"))
	if err == nil || !strings.Contains(err.Error(), `duplicate app "api"`) || !strings.Contains(err.Error(), "one.yaml") || !strings.Contains(err.Error(), "two.yaml") {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadRejectsCircularResourcesWithChain(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "heron.yaml")
	one := filepath.Join(dir, "one.yaml")
	writeConfigFile(t, root, "resources:\n  - one.yaml\n")
	writeConfigFile(t, one, "resources:\n  - heron.yaml\n")

	_, err := Load(root)
	canonicalRoot := canonicalTestPath(t, root)
	canonicalOne := canonicalTestPath(t, one)
	if err == nil || !strings.Contains(err.Error(), "circular resource include") || !strings.Contains(err.Error(), canonicalRoot+" -> "+canonicalOne+" -> "+canonicalRoot) {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadRejectsGlobalFieldsInResource(t *testing.T) {
	dir := t.TempDir()
	writeConfigFile(t, filepath.Join(dir, "heron.yaml"), "resources:\n  - apps.yaml\n")
	writeConfigFile(t, filepath.Join(dir, "apps.yaml"), "port: 4000\napps: {}\n")

	_, err := Load(filepath.Join(dir, "heron.yaml"))
	if err == nil || !strings.Contains(err.Error(), `resource cannot set global field "port"`) {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadStrictRejectsUnknownResourceField(t *testing.T) {
	dir := t.TempDir()
	writeConfigFile(t, filepath.Join(dir, "heron.yaml"), "resources:\n  - apps.yaml\n")
	writeConfigFile(t, filepath.Join(dir, "apps.yaml"), "appps: {}\n")

	_, err := LoadStrict(filepath.Join(dir, "heron.yaml"))
	if err == nil || !strings.Contains(err.Error(), `unknown field "appps"`) {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadReportsMissingResourceWithDeclaringFile(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "heron.yaml")
	writeConfigFile(t, root, "resources:\n  - missing.yaml\n")

	_, err := Load(root)
	if err == nil || !strings.Contains(err.Error(), `load resource "missing.yaml" declared in `+canonicalTestPath(t, root)) {
		t.Fatalf("error = %v", err)
	}
}

func writeConfigFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}
}

func canonicalTestPath(t *testing.T, path string) string {
	t.Helper()
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}
