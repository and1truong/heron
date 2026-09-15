package supervisor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/and1truong/heron/internal/config"
	proc "github.com/and1truong/heron/internal/process"
)

type reloadRunner struct {
	*dependencyRunner
	specs []proc.CommandSpec
}

func (r *reloadRunner) Run(ctx context.Context, spec proc.CommandSpec) error {
	r.specs = append(r.specs, spec)
	return r.dependencyRunner.Run(ctx, spec)
}

func (r *reloadRunner) Start(ctx context.Context, spec proc.CommandSpec) (*proc.Process, error) {
	r.specs = append(r.specs, spec)
	return r.dependencyRunner.Start(ctx, spec)
}

func TestRestartReloadsConfigFromDisk(t *testing.T) {
	r := &dependencyRunner{}
	s := dependencySupervisor(t, map[string][]string{"api": nil, "other": nil}, r)
	recorder := &reloadRunner{dependencyRunner: r}
	s.Service("api").runner = recorder
	startDependencyApp(t, s, "api")
	startDependencyApp(t, s, "other")
	path := filepath.Join(t.TempDir(), "heron.yaml")
	if err := os.WriteFile(path, []byte("apps:\n  api:\n    pwd: /tmp\n    launch: new-launch\n    stop: new-stop\n    env:\n      VERSION: new\n"), 0600); err != nil {
		t.Fatal(err)
	}
	s.SetConfigLoader(func() (config.RuntimeConfig, error) { return config.Load(path) })
	if err := s.Action(context.Background(), "api", "restart"); err != nil {
		t.Fatal(err)
	}
	got := s.Service("api").Config()
	if got.Launch != "new-launch" || got.Stop != "new-stop" || got.Env["VERSION"] != "new" {
		t.Fatalf("config not refreshed: %+v", got)
	}
	if s.Service("other").Config().Launch != "launch" {
		t.Fatal("unrelated service changed")
	}
	if !reflect.DeepEqual(r.events, []string{"start:api", "start:other", "stop:api", "start:api"}) {
		t.Fatal(r.events)
	}
	if len(recorder.specs) != 3 || recorder.specs[1].Command != "stop" || recorder.specs[2].Command != "new-launch" || recorder.specs[2].Env["VERSION"] != "new" {
		t.Fatalf("expected old stop command and new launch environment: %+v", recorder.specs)
	}
	if err := os.WriteFile(path, []byte("apps: [invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.Action(context.Background(), "api", "restart"); err == nil {
		t.Fatal("invalid config accepted")
	}
	if s.Service("api").State() != StateRunning || len(r.events) != 4 {
		t.Fatal("invalid config stopped running service")
	}
}

func TestRestartRejectsTopologyChangesBeforeStop(t *testing.T) {
	for _, change := range []string{"endpoint", "dependency", "removed", "load-error"} {
		t.Run(change, func(t *testing.T) {
			r := &dependencyRunner{}
			s := dependencySupervisor(t, map[string][]string{"api": nil}, r)
			startDependencyApp(t, s, "api")
			fresh := s.Service("api").Config()
			s.SetConfigLoader(func() (config.RuntimeConfig, error) {
				cfg := config.RuntimeConfig{Apps: map[string]config.RuntimeAppConfig{}}
				switch change {
				case "endpoint":
					fresh.Port = 9000
				case "dependency":
					fresh.DependsOn = []string{"db"}
				case "removed":
					return cfg, nil
				case "load-error":
					return cfg, errors.New("bad config")
				}
				cfg.Apps["api"] = fresh
				return cfg, nil
			})
			if err := s.Action(context.Background(), "api", "restart"); err == nil {
				t.Fatal("change accepted")
			}
			if len(r.events) != 1 || s.Service("api").State() != StateRunning {
				t.Fatal("service disrupted")
			}
		})
	}
}
