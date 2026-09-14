package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type Duration struct {
	time.Duration
	set bool
}

func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	v, err := time.ParseDuration(n.Value)
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", n.Value, err)
	}
	d.Duration = v
	d.set = true
	return nil
}

type Config struct {
	Resources      []string                       `yaml:"resources,omitempty"`
	Port           int                            `yaml:"port"`
	Idle           Duration                       `yaml:"idle"`
	LogLevel       string                         `yaml:"logLevel"`
	StartTimeout   Duration                       `yaml:"startTimeout"`
	StopTimeout    Duration                       `yaml:"stopTimeout"`
	StartUp        []string                       `yaml:"startUp"`
	TearDown       []string                       `yaml:"tearDown"`
	ScheduledTasks map[string]ScheduledTaskConfig `yaml:"scheduledTasks,omitempty"`
	Apps           map[string]AppConfig           `yaml:"apps"`
}
type ScheduledTaskConfig struct {
	Command    string   `yaml:"command"`
	Every      Duration `yaml:"every"`
	Timeout    Duration `yaml:"timeout"`
	RunOnStart bool     `yaml:"runOnStart"`
	Overlap    string   `yaml:"overlap"`
}
type AppConfig struct {
	DependsOn     []string                  `yaml:"dependsOn,omitempty"`
	EnvFiles      []string                  `yaml:"envFiles,omitempty"`
	Pwd           string                    `yaml:"pwd"`
	Build         string                    `yaml:"build"`
	Launch        string                    `yaml:"launch"`
	Stop          string                    `yaml:"stop"`
	Env           map[string]string         `yaml:"env,omitempty"`
	Protocol      string                    `yaml:"protocol"`
	Path          string                    `yaml:"path"`
	Host          string                    `yaml:"host"`
	ListenPort    int                       `yaml:"listenPort"`
	Port          int                       `yaml:"port"`
	Idle          *Duration                 `yaml:"idle"`
	IncludePrefix bool                      `yaml:"includePrefix"`
	Health        HealthConfig              `yaml:"health"`
	Endpoints     map[string]EndpointConfig `yaml:"endpoints,omitempty"`
}
type EndpointConfig struct {
	Port          int          `yaml:"port"`
	Protocol      string       `yaml:"protocol"`
	Path          string       `yaml:"path"`
	Host          string       `yaml:"host"`
	ListenPort    int          `yaml:"listenPort"`
	IncludePrefix bool         `yaml:"includePrefix"`
	Health        HealthConfig `yaml:"health"`
	Primary       bool         `yaml:"primary"`
}
type HealthConfig struct {
	GRPC bool `yaml:"grpc"`
}
type RuntimeConfig struct {
	Port                      int
	LogLevel                  string
	StartTimeout, StopTimeout time.Duration
	StartUp, TearDown         []string
	ScheduledTasks            map[string]RuntimeScheduledTaskConfig
	Apps                      map[string]RuntimeAppConfig
}
type RuntimeScheduledTaskConfig struct {
	Name, Command, Overlap string
	Every, Timeout         time.Duration
	RunOnStart             bool
}
type RuntimeAppConfig struct {
	DependsOn                                          []string
	ID, Pwd, Build, Launch, Stop, Protocol, Path, Host string
	Source                                             string
	Env                                                map[string]string
	ListenPort, Port                                   int
	Idle, StartTimeout, StopTimeout                    time.Duration
	IncludePrefix                                      bool
	GRPCHealth                                         bool
	Endpoints                                          map[string]RuntimeEndpointConfig
}
type RuntimeEndpointConfig struct {
	Name, Protocol, Path, Host string
	Aliases                    []string
	ListenPort, Port           int
	IncludePrefix, GRPCHealth  bool
	Primary                    bool
}

// EndpointList returns an app's endpoints in stable name order. Runtime
// configs constructed by older callers are represented as one legacy endpoint.
func (a RuntimeAppConfig) EndpointList() []RuntimeEndpointConfig {
	if len(a.Endpoints) == 0 {
		if a.Port == 0 {
			return nil
		}
		return []RuntimeEndpointConfig{{Name: "default", Protocol: a.Protocol, Path: a.Path, Host: a.Host, ListenPort: a.ListenPort, Port: a.Port, IncludePrefix: a.IncludePrefix, GRPCHealth: a.GRPCHealth, Primary: true}}
	}
	names := make([]string, 0, len(a.Endpoints))
	for name := range a.Endpoints {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make([]RuntimeEndpointConfig, 0, len(names))
	for _, name := range names {
		result = append(result, a.Endpoints[name])
	}
	return result
}

const (
	ProtocolHTTP = "http"
	ProtocolGRPC = "grpc"
	ProtocolTCP  = "tcp"
)

func DefaultPath() (string, error) {
	h, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(h, ".config", "heron.yaml"), nil
}

func expandHomePath(path string) (string, error) {
	if path != "~" && !strings.HasPrefix(path, "~/") && !strings.HasPrefix(path, `~\`) {
		return path, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	if path == "~" {
		return home, nil
	}
	return filepath.Join(home, path[2:]), nil
}

func normalizeHost(host string) (string, error) {
	host = strings.TrimSpace(host)
	if host == "" || strings.ContainsAny(host, " /\\@") || strings.Contains(host, ":") {
		return "", fmt.Errorf("must be a hostname without a port")
	}
	return strings.ToLower(host), nil
}

func Load(path string) (RuntimeConfig, error) {
	return load(path, false)
}

// LoadStrict loads a configuration while rejecting unknown YAML fields. It is
// intended for diagnostics that should catch misspelled or obsolete options.
func LoadStrict(path string) (RuntimeConfig, error) {
	return load(path, true)
}

func load(path string, strict bool) (RuntimeConfig, error) {
	return loadWithReader(path, strict, os.ReadFile)
}

// ValidateOverlay validates a prospective source edit without writing or executing it.
func ValidateOverlay(root, source string, content []byte) error {
	canonical, err := CanonicalPath(source)
	if err != nil {
		return err
	}
	used := false
	_, err = loadWithReader(root, true, func(path string) ([]byte, error) {
		if path == canonical {
			used = true
			return content, nil
		}
		return os.ReadFile(path)
	})
	if err == nil && !used {
		return fmt.Errorf("configuration source is no longer included; restart Heron before editing")
	}
	return err
}

func loadWithReader(path string, strict bool, readFile func(string) ([]byte, error)) (RuntimeConfig, error) {
	root, err := CanonicalPath(path)
	if err != nil {
		return RuntimeConfig{}, err
	}
	b, err := readFile(root)
	if err != nil {
		return RuntimeConfig{}, err
	}
	var c Config
	decoder := yaml.NewDecoder(bytes.NewReader(b))
	decoder.KnownFields(strict)
	if err := decoder.Decode(&c); err != nil {
		return RuntimeConfig{}, fmt.Errorf("decode %s: %w", root, err)
	}
	sources := make(map[string]string, len(c.Apps))
	for id := range c.Apps {
		sources[id] = root
	}
	loader := resourceLoader{strict: strict, apps: c.Apps, sources: sources, readFile: readFile}
	if loader.apps == nil {
		loader.apps = make(map[string]AppConfig)
	}
	if err := loader.loadAll(c.Resources, root, []string{root}); err != nil {
		return RuntimeConfig{}, err
	}
	c.Apps = loader.apps
	runtime, err := c.Normalize()
	if err != nil {
		return RuntimeConfig{}, err
	}
	for id, source := range loader.sources {
		app := runtime.Apps[id]
		app.Source = source
		runtime.Apps[id] = app
	}
	return runtime, nil
}

type resourceConfig struct {
	Resources []string             `yaml:"resources,omitempty"`
	Apps      map[string]AppConfig `yaml:"apps,omitempty"`
}

type resourceLoader struct {
	readFile func(string) ([]byte, error)
	strict   bool
	apps     map[string]AppConfig
	sources  map[string]string
}

var globalFields = map[string]struct{}{
	"port": {}, "idle": {}, "logLevel": {}, "startTimeout": {},
	"stopTimeout": {}, "startUp": {}, "tearDown": {}, "scheduledTasks": {},
}

// CanonicalPath expands a leading home directory marker and resolves the path
// to the same canonical form used for configuration source tracking.
func CanonicalPath(path string) (string, error) {
	expanded, err := expandHomePath(path)
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(expanded)
	if err != nil {
		return "", err
	}
	canonical, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	return canonical, nil
}

func (l *resourceLoader) loadAll(resources []string, declaringFile string, stack []string) error {
	for _, resource := range resources {
		resource = strings.TrimSpace(resource)
		if resource == "" {
			return fmt.Errorf("resource in %s has an empty path", declaringFile)
		}
		path, err := expandHomePath(resource)
		if err != nil {
			return fmt.Errorf("load resource %q declared in %s: %w", resource, declaringFile, err)
		}
		if !filepath.IsAbs(path) {
			path = filepath.Join(filepath.Dir(declaringFile), path)
		}
		canonical, err := CanonicalPath(path)
		if err != nil {
			return fmt.Errorf("load resource %q declared in %s: %w", resource, declaringFile, err)
		}
		for i, ancestor := range stack {
			if ancestor == canonical {
				chain := append(append([]string(nil), stack[i:]...), canonical)
				return fmt.Errorf("circular resource include: %s", strings.Join(chain, " -> "))
			}
		}
		if err := l.loadOne(canonical, append(stack, canonical)); err != nil {
			return fmt.Errorf("resource %s: %w", canonical, err)
		}
	}
	return nil
}

func (l *resourceLoader) loadOne(path string, stack []string) error {
	read := l.readFile
	if read == nil {
		read = os.ReadFile
	}
	b, err := read(path)
	if err != nil {
		return err
	}
	var document yaml.Node
	if err := yaml.Unmarshal(b, &document); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	if err := validateResourceFields(&document, path, l.strict); err != nil {
		return err
	}
	var resource resourceConfig
	decoder := yaml.NewDecoder(bytes.NewReader(b))
	decoder.KnownFields(l.strict)
	if err := decoder.Decode(&resource); err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	for id, app := range resource.Apps {
		if first, exists := l.sources[id]; exists {
			return fmt.Errorf("duplicate app %q: first defined in %s, redefined in %s", id, first, path)
		}
		l.apps[id] = app
		l.sources[id] = path
	}
	return l.loadAll(resource.Resources, path, stack)
}

func validateResourceFields(document *yaml.Node, path string, strict bool) error {
	if len(document.Content) == 0 {
		return nil
	}
	root := document.Content[0]
	if root.Kind != yaml.MappingNode {
		return fmt.Errorf("%s: resource must be a YAML mapping", path)
	}
	for i := 0; i < len(root.Content); i += 2 {
		field := root.Content[i].Value
		if field == "resources" || field == "apps" {
			continue
		}
		if _, global := globalFields[field]; global {
			return fmt.Errorf("%s: resource cannot set global field %q", path, field)
		}
		if strict {
			return fmt.Errorf("%s: unknown field %q", path, field)
		}
	}
	return nil
}
func (c Config) Normalize() (RuntimeConfig, error) {
	if c.Port == 0 {
		c.Port = 3000
	}
	if !c.Idle.set && c.Idle.Duration == 0 {
		c.Idle.Duration = 30 * time.Minute
	}
	if !c.StartTimeout.set && c.StartTimeout.Duration == 0 {
		c.StartTimeout.Duration = 30 * time.Second
	}
	if !c.StopTimeout.set && c.StopTimeout.Duration == 0 {
		c.StopTimeout.Duration = 10 * time.Second
	}
	if c.LogLevel == "" {
		c.LogLevel = "info"
	}
	if c.Port < 1 || c.Port > 65535 {
		return RuntimeConfig{}, fmt.Errorf("invalid wrapper port %d", c.Port)
	}
	switch c.LogLevel {
	case "debug", "info", "warning", "warn", "error":
	default:
		return RuntimeConfig{}, fmt.Errorf("invalid log level %q", c.LogLevel)
	}
	if c.Idle.Duration <= 0 || c.StartTimeout.Duration <= 0 || c.StopTimeout.Duration <= 0 {
		return RuntimeConfig{}, fmt.Errorf("durations must be positive")
	}
	r := RuntimeConfig{
		Port:           c.Port,
		LogLevel:       c.LogLevel,
		StartTimeout:   c.StartTimeout.Duration,
		StopTimeout:    c.StopTimeout.Duration,
		StartUp:        append([]string(nil), c.StartUp...),
		TearDown:       append([]string(nil), c.TearDown...),
		ScheduledTasks: make(map[string]RuntimeScheduledTaskConfig, len(c.ScheduledTasks)),
		Apps:           make(map[string]RuntimeAppConfig, len(c.Apps)),
	}
	for name, task := range c.ScheduledTasks {
		if strings.TrimSpace(name) == "" {
			return RuntimeConfig{}, fmt.Errorf("scheduled task name is required")
		}
		if strings.TrimSpace(task.Command) == "" {
			return RuntimeConfig{}, fmt.Errorf("scheduled task %q: command is required", name)
		}
		if task.Every.Duration <= 0 {
			return RuntimeConfig{}, fmt.Errorf("scheduled task %q: every must be positive", name)
		}
		if !task.Timeout.set && task.Timeout.Duration == 0 {
			task.Timeout.Duration = 5 * time.Minute
		}
		if task.Timeout.Duration <= 0 {
			return RuntimeConfig{}, fmt.Errorf("scheduled task %q: timeout must be positive", name)
		}
		if task.Overlap == "" {
			task.Overlap = "skip"
		}
		if task.Overlap != "skip" {
			return RuntimeConfig{}, fmt.Errorf("scheduled task %q: invalid overlap %q (only skip is supported)", name, task.Overlap)
		}
		r.ScheduledTasks[name] = RuntimeScheduledTaskConfig{
			Name: name, Command: task.Command, Every: task.Every.Duration,
			Timeout: task.Timeout.Duration, RunOnStart: task.RunOnStart, Overlap: task.Overlap,
		}
	}
	paths, hosts, ports, listenPorts := map[string]string{}, map[string]string{}, map[int]string{}, map[int]string{}
	for id, a := range c.Apps {
		if strings.TrimSpace(a.Launch) == "" {
			return RuntimeConfig{}, fmt.Errorf("app %q: launch is required", id)
		}
		endpointInputs, err := appEndpointInputs(id, a)
		if err != nil {
			return RuntimeConfig{}, err
		}
		runtimeEndpoints := make(map[string]RuntimeEndpointConfig, len(endpointInputs))
		for name, endpoint := range endpointInputs {
			normalized, err := normalizeEndpoint(id, name, endpoint, len(endpointInputs), c.Port, paths, hosts, ports, listenPorts)
			if err != nil {
				return RuntimeConfig{}, err
			}
			runtimeEndpoints[name] = normalized
		}
		if err := validateEnvironment(a.Env); err != nil {
			return RuntimeConfig{}, fmt.Errorf("app %q: %w", id, err)
		}
		if a.Pwd == "" {
			return RuntimeConfig{}, fmt.Errorf("app %q: pwd is required", id)
		}
		pwd, err := expandHomePath(a.Pwd)
		if err != nil {
			return RuntimeConfig{}, fmt.Errorf("app %q: expand pwd: %w", id, err)
		}
		st, err := os.Stat(pwd)
		if err != nil {
			return RuntimeConfig{}, fmt.Errorf("app %q: pwd: %w", id, err)
		}
		if !st.IsDir() {
			return RuntimeConfig{}, fmt.Errorf("app %q: pwd is not a directory", id)
		}
		a.Pwd = pwd
		resolvedEnv, err := resolveEnvironment(a)
		if err != nil {
			return RuntimeConfig{}, fmt.Errorf("app %q: %w", id, err)
		}
		idle := c.Idle.Duration
		if a.Idle != nil {
			idle = a.Idle.Duration
		}
		if idle < 0 {
			return RuntimeConfig{}, fmt.Errorf("app %q: idle must be non-negative", id)
		}
		runtimeApp := RuntimeAppConfig{ID: id, DependsOn: append([]string(nil), a.DependsOn...), Pwd: a.Pwd, Build: a.Build, Launch: a.Launch, Stop: a.Stop, Env: resolvedEnv, Idle: idle, StartTimeout: r.StartTimeout, StopTimeout: r.StopTimeout, Endpoints: runtimeEndpoints}
		for _, endpoint := range runtimeEndpoints {
			if endpoint.Primary {
				runtimeApp.Protocol, runtimeApp.Path, runtimeApp.Host = endpoint.Protocol, endpoint.Path, endpoint.Host
				runtimeApp.ListenPort, runtimeApp.Port = endpoint.ListenPort, endpoint.Port
				runtimeApp.IncludePrefix, runtimeApp.GRPCHealth = endpoint.IncludePrefix, endpoint.GRPCHealth
				break
			}
		}
		r.Apps[id] = runtimeApp
	}
	if _, err := r.DependencyOrder(); err != nil {
		return RuntimeConfig{}, err
	}
	return r, nil
}

func appEndpointInputs(id string, app AppConfig) (map[string]EndpointConfig, error) {
	if len(app.Endpoints) == 0 {
		if app.Port == 0 {
			if app.Protocol != "" || app.Path != "" || app.Host != "" || app.ListenPort != 0 || app.IncludePrefix || app.Health.GRPC {
				return nil, fmt.Errorf("app %q: invalid port 0: routing fields require port or endpoints", id)
			}
			return map[string]EndpointConfig{}, nil
		}
		return map[string]EndpointConfig{"default": {Port: app.Port, Protocol: app.Protocol, Path: app.Path, Host: app.Host, ListenPort: app.ListenPort, IncludePrefix: app.IncludePrefix, Health: app.Health, Primary: true}}, nil
	}
	if app.Port != 0 || app.Protocol != "" || app.Path != "" || app.Host != "" || app.ListenPort != 0 || app.IncludePrefix || app.Health.GRPC {
		return nil, fmt.Errorf("app %q: legacy routing fields cannot be combined with endpoints", id)
	}
	if !validDNSLabel(id) {
		return nil, fmt.Errorf("app %q: app ID must be a valid DNS label when using endpoints", id)
	}
	primary := 0
	result := make(map[string]EndpointConfig, len(app.Endpoints))
	for name, endpoint := range app.Endpoints {
		if !validDNSLabel(name) {
			return nil, fmt.Errorf("app %q: endpoint %q is not a valid DNS label", id, name)
		}
		name = strings.ToLower(name)
		if _, exists := result[name]; exists {
			return nil, fmt.Errorf("app %q: duplicate endpoint name %q", id, name)
		}
		if endpoint.Primary {
			primary++
		}
		result[name] = endpoint
	}
	if len(result) == 1 && primary == 0 {
		for name, endpoint := range result {
			endpoint.Primary = true
			result[name] = endpoint
		}
		primary = 1
	}
	if primary != 1 {
		return nil, fmt.Errorf("app %q: endpoints require exactly one primary endpoint", id)
	}
	return result, nil
}

func normalizeEndpoint(appID, name string, endpoint EndpointConfig, count, wrapperPort int, paths, hosts map[string]string, ports, listenPorts map[int]string) (RuntimeEndpointConfig, error) {
	label := appID
	if count > 1 || name != "default" {
		label += "." + name
	}
	if endpoint.Port < 1 || endpoint.Port > 65535 {
		return RuntimeEndpointConfig{}, fmt.Errorf("app %q endpoint %q: invalid port %d", appID, name, endpoint.Port)
	}
	if endpoint.Port == wrapperPort {
		return RuntimeEndpointConfig{}, fmt.Errorf("app %q endpoint %q: backend port %d conflicts with wrapper port", appID, name, endpoint.Port)
	}
	if prior, ok := listenPorts[endpoint.Port]; ok {
		return RuntimeEndpointConfig{}, fmt.Errorf("endpoint %q backend port %d conflicts with endpoint %q listenPort", label, endpoint.Port, prior)
	}
	if prior, ok := ports[endpoint.Port]; ok {
		return RuntimeEndpointConfig{}, fmt.Errorf("endpoints %q and %q use duplicate port %d", prior, label, endpoint.Port)
	}
	ports[endpoint.Port] = label
	protocol := strings.ToLower(strings.TrimSpace(endpoint.Protocol))
	if protocol == "" {
		protocol = ProtocolHTTP
	}
	path, host := strings.TrimSpace(endpoint.Path), strings.TrimSpace(endpoint.Host)
	result := RuntimeEndpointConfig{Name: name, Protocol: protocol, Port: endpoint.Port, ListenPort: endpoint.ListenPort, IncludePrefix: endpoint.IncludePrefix, GRPCHealth: endpoint.Health.GRPC, Primary: endpoint.Primary}
	registerHost := func(value string) error {
		if prior, ok := hosts[value]; ok {
			return fmt.Errorf("endpoints %q and %q use duplicate host %q", prior, label, value)
		}
		hosts[value] = label
		return nil
	}
	switch protocol {
	case ProtocolHTTP:
		if endpoint.ListenPort != 0 {
			return RuntimeEndpointConfig{}, fmt.Errorf("app %q endpoint %q: listenPort is only supported with TCP", appID, name)
		}
		generatedHost := path == "" && host == ""
		if generatedHost {
			if endpoint.Primary {
				host = appID + ".localhost"
				alias := name + "." + appID + ".localhost"
				if alias != host {
					result.Aliases = []string{alias}
				}
			} else {
				host = name + "." + appID + ".localhost"
			}
		}
		if (path == "") == (host == "") {
			return RuntimeEndpointConfig{}, fmt.Errorf("app %q endpoint %q: exactly one of path or host is required", appID, name)
		}
		if host != "" {
			if endpoint.IncludePrefix {
				return RuntimeEndpointConfig{}, fmt.Errorf("app %q endpoint %q: includePrefix is not supported with host routing", appID, name)
			}
			var err error
			host, err = normalizeHost(host)
			if err != nil {
				return RuntimeEndpointConfig{}, fmt.Errorf("app %q endpoint %q: host: %w", appID, name, err)
			}
			if err := registerHost(host); err != nil {
				return RuntimeEndpointConfig{}, err
			}
			for i, alias := range result.Aliases {
				alias, err = normalizeHost(alias)
				if err != nil {
					return RuntimeEndpointConfig{}, fmt.Errorf("app %q endpoint %q: alias: %w", appID, name, err)
				}
				result.Aliases[i] = alias
				if err := registerHost(alias); err != nil {
					return RuntimeEndpointConfig{}, err
				}
			}
		} else {
			if !strings.HasPrefix(path, "/") {
				return RuntimeEndpointConfig{}, fmt.Errorf("app %q endpoint %q: path must start with /", appID, name)
			}
			if path != "/" {
				path = strings.TrimRight(path, "/")
			}
			if prior, ok := paths[path]; ok {
				return RuntimeEndpointConfig{}, fmt.Errorf("endpoints %q and %q use duplicate path %q", prior, label, path)
			}
			paths[path] = label
		}
	case ProtocolGRPC:
		if endpoint.ListenPort != 0 {
			return RuntimeEndpointConfig{}, fmt.Errorf("app %q endpoint %q: listenPort is only supported with TCP", appID, name)
		}
		if path != "" || endpoint.IncludePrefix {
			return RuntimeEndpointConfig{}, fmt.Errorf("app %q endpoint %q: path and includePrefix are not supported with gRPC", appID, name)
		}
		if host == "" {
			if endpoint.Primary {
				host = appID + ".localhost"
				result.Aliases = []string{name + "." + appID + ".localhost"}
			} else {
				host = name + "." + appID + ".localhost"
			}
		}
		var err error
		host, err = normalizeHost(host)
		if err != nil {
			return RuntimeEndpointConfig{}, fmt.Errorf("app %q endpoint %q: host: %w", appID, name, err)
		}
		if err := registerHost(host); err != nil {
			return RuntimeEndpointConfig{}, err
		}
		for i, alias := range result.Aliases {
			alias, err = normalizeHost(alias)
			if err != nil {
				return RuntimeEndpointConfig{}, fmt.Errorf("app %q endpoint %q: alias: %w", appID, name, err)
			}
			result.Aliases[i] = alias
			if alias != host {
				if err := registerHost(alias); err != nil {
					return RuntimeEndpointConfig{}, err
				}
			}
		}
	case ProtocolTCP:
		if path != "" || host != "" || endpoint.IncludePrefix {
			return RuntimeEndpointConfig{}, fmt.Errorf("app %q endpoint %q: path, host, and includePrefix are not supported with TCP", appID, name)
		}
		if endpoint.ListenPort < 1 || endpoint.ListenPort > 65535 {
			return RuntimeEndpointConfig{}, fmt.Errorf("app %q endpoint %q: invalid listenPort %d", appID, name, endpoint.ListenPort)
		}
		if endpoint.ListenPort == wrapperPort {
			return RuntimeEndpointConfig{}, fmt.Errorf("app %q endpoint %q: listenPort %d conflicts with wrapper port", appID, name, endpoint.ListenPort)
		}
		if prior, ok := ports[endpoint.ListenPort]; ok {
			return RuntimeEndpointConfig{}, fmt.Errorf("endpoint %q listenPort %d conflicts with endpoint %q backend port", label, endpoint.ListenPort, prior)
		}
		if prior, ok := listenPorts[endpoint.ListenPort]; ok {
			return RuntimeEndpointConfig{}, fmt.Errorf("endpoints %q and %q use duplicate listenPort %d", prior, label, endpoint.ListenPort)
		}
		listenPorts[endpoint.ListenPort] = label
	default:
		return RuntimeEndpointConfig{}, fmt.Errorf("app %q endpoint %q: invalid protocol %q", appID, name, endpoint.Protocol)
	}
	if endpoint.Health.GRPC && protocol != ProtocolGRPC {
		return RuntimeEndpointConfig{}, fmt.Errorf("app %q endpoint %q: health.grpc is only supported with gRPC", appID, name)
	}
	result.Path, result.Host = path, host
	return result, nil
}

func validDNSLabel(value string) bool {
	if value == "" || len(value) > 63 || value[0] == '-' || value[len(value)-1] == '-' {
		return false
	}
	for _, r := range value {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '-' {
			return false
		}
	}
	return true
}

func cloneStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func validateEnvironment(values map[string]string) error {
	for key, value := range values {
		if key == "" || strings.ContainsAny(key, "=\x00") {
			return fmt.Errorf("invalid environment variable name %q", key)
		}
		if strings.ContainsRune(value, '\x00') {
			return fmt.Errorf("environment variable %q contains NUL", key)
		}
	}
	return nil
}
