package supervisor

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/and1truong/heron/internal/config"
	proc "github.com/and1truong/heron/internal/process"
)

type dependencyRunner struct {
	stopFail bool
	mu       sync.Mutex
	events   []string
	fail     string
	block    string
	entered  chan struct{}
}

func (r *dependencyRunner) Run(ctx context.Context, spec proc.CommandSpec) error {
	r.mu.Lock()
	r.events = append(r.events, spec.Kind+":"+spec.Service)
	failed := r.stopFail
	r.mu.Unlock()
	if failed {
		return errors.New("stop failed")
	}
	return nil
}
func (r *dependencyRunner) Start(ctx context.Context, spec proc.CommandSpec) (*proc.Process, error) {
	r.mu.Lock()
	r.events = append(r.events, "start:"+spec.Service)
	r.mu.Unlock()
	if spec.Service == r.fail {
		return nil, errors.New("injected failure")
	}
	if spec.Service == r.block {
		close(r.entered)
		<-ctx.Done()
		return nil, ctx.Err()
	}
	done := make(chan struct{})
	close(done)
	return &proc.Process{Done: done}, nil // successful daemon launcher
}
func dependencySupervisor(t *testing.T, graph map[string][]string, r *dependencyRunner) *Supervisor {
	t.Helper()
	cfg := config.RuntimeConfig{Apps: map[string]config.RuntimeAppConfig{}}
	for id, deps := range graph {
		cfg.Apps[id] = config.RuntimeAppConfig{ID: id, DependsOn: deps, Launch: "launch", Stop: "stop", StartTimeout: time.Second, StopTimeout: time.Second, Idle: time.Hour}
	}
	s := New(cfg, r, slog.New(slog.NewTextHandler(io.Discard, nil)))
	t.Cleanup(func() { _ = s.StopAll(context.Background()) })
	return s
}
func startDependencyApp(t *testing.T, s *Supervisor, id string) {
	t.Helper()
	release, err := s.Acquire(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	release()
}
func TestDependencyLifetimeAndStopOrder(t *testing.T) {
	r := &dependencyRunner{}
	s := dependencySupervisor(t, map[string][]string{"web": {"api"}, "api": {"db"}, "db": nil}, r)
	startDependencyApp(t, s, "web")
	if !reflect.DeepEqual(r.events, []string{"start:db", "start:api", "start:web"}) {
		t.Fatal(r.events)
	}
	for _, id := range []string{"db", "api"} {
		v := s.Service(id)
		v.mu.Lock()
		g := v.generation
		v.mu.Unlock()
		v.idle(g)
		if v.State() != StateRunning {
			t.Fatalf("%s idle-stopped", id)
		}
		for _, action := range []string{"stop", "kill", "restart"} {
			if err := s.Action(context.Background(), id, action); err == nil || !strings.Contains(err.Error(), "active dependents") {
				t.Fatalf("%s %s: %v", action, id, err)
			}
		}
		if err := v.Stop(context.Background()); err == nil {
			t.Fatal("direct Stop bypassed leases")
		}
	}
	if err := s.StopAll(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(r.events, []string{"start:db", "start:api", "start:web", "stop:web", "stop:api", "stop:db"}) {
		t.Fatal(r.events)
	}
}
func TestSharedDependenciesConcurrentStartup(t *testing.T) {
	r := &dependencyRunner{}
	s := dependencySupervisor(t, map[string][]string{"a": {"db"}, "c": {"db"}, "db": nil}, r)
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := "a"
			if i%2 == 0 {
				id = "c"
			}
			release, err := s.Acquire(context.Background(), id)
			if err != nil {
				t.Error(err)
				return
			}
			release()
		}(i)
	}
	wg.Wait()
	if len(r.events) != 3 {
		t.Fatal(r.events)
	}
	if err := s.Action(context.Background(), "a", "stop"); err != nil {
		t.Fatal(err)
	}
	db := s.Service("db")
	db.mu.Lock()
	ids := db.dependentIDsLocked()
	db.mu.Unlock()
	if !reflect.DeepEqual(ids, []string{"c"}) {
		t.Fatal(ids)
	}
	if err := s.Action(context.Background(), "c", "stop"); err != nil {
		t.Fatal(err)
	}
	db.mu.Lock()
	g := db.generation
	count := len(db.dependents)
	timer := db.idleTimer
	db.mu.Unlock()
	if count != 0 || timer == nil {
		t.Fatalf("leases=%d timer=%v", count, timer)
	}
	db.idle(g)
	if err := db.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if db.State() != StateStopped {
		t.Fatal(db.State())
	}
}
func TestDependencyStartupFailuresReleaseLeases(t *testing.T) {
	for _, failed := range []string{"a", "db"} {
		t.Run(failed, func(t *testing.T) {
			r := &dependencyRunner{fail: failed}
			s := dependencySupervisor(t, map[string][]string{"a": {"db"}, "db": nil}, r)
			_, err := s.Acquire(context.Background(), "a")
			if err == nil {
				t.Fatal("expected failure")
			}
			a := s.Service("a")
			a.mu.Lock()
			done := a.workerDone
			a.mu.Unlock()
			<-done
			db := s.Service("db")
			db.mu.Lock()
			count := len(db.dependents)
			db.mu.Unlock()
			if count != 0 {
				t.Fatal("leaked lease")
			}
			if failed == "db" && (!strings.Contains(err.Error(), "dependency \"db\"") || len(r.events) != 1) {
				t.Fatalf("events=%v err=%v", r.events, err)
			}
		})
	}
}
func TestCancelledDependencyStartupReleasesLeases(t *testing.T) {
	r := &dependencyRunner{block: "a", entered: make(chan struct{})}
	s := dependencySupervisor(t, map[string][]string{"a": {"db"}, "db": nil}, r)
	result := make(chan error, 1)
	go func() { _, err := s.Acquire(context.Background(), "a"); result <- err }()
	<-r.entered
	if err := s.Action(context.Background(), "a", "stop"); err != nil {
		t.Fatal(err)
	}
	if err := <-result; err == nil {
		t.Fatal("expected cancellation")
	}
	db := s.Service("db")
	db.mu.Lock()
	count := len(db.dependents)
	db.mu.Unlock()
	if count != 0 {
		t.Fatal("leaked lease")
	}
}
func TestUnexpectedExitReleasesDependencies(t *testing.T) {
	r := &dependencyRunner{}
	s := dependencySupervisor(t, map[string][]string{"a": {"db"}, "db": nil}, r)
	startDependencyApp(t, s, "a")
	a := s.Service("a")
	a.mu.Lock()
	p := a.process
	a.mu.Unlock()
	a.watch(p)
	db := s.Service("db")
	db.mu.Lock()
	count := len(db.dependents)
	db.mu.Unlock()
	if count != 0 {
		t.Fatal("leaked lease")
	}
}

func TestFailedStopRetainsDependencyLease(t *testing.T) {
	r := &dependencyRunner{}
	s := dependencySupervisor(t, map[string][]string{"a": {"db"}, "db": nil}, r)
	startDependencyApp(t, s, "a")
	snapshots := s.Snapshots()
	if !reflect.DeepEqual(snapshots[1].ActiveDependents, []string{"a"}) || !reflect.DeepEqual(snapshots[0].DependsOn, []string{"db"}) {
		t.Fatal(snapshots)
	}
	r.mu.Lock()
	r.stopFail = true
	r.mu.Unlock()
	if err := s.Action(context.Background(), "a", "stop"); err == nil {
		t.Fatal("expected stop failure")
	}
	if err := s.Action(context.Background(), "a", "start"); err == nil {
		t.Fatal("restarted after failed stop")
	}
	if err := s.Service("db").Stop(context.Background()); err == nil {
		t.Fatal("dependency stopped after dependent stop failed")
	}
	r.mu.Lock()
	r.stopFail = false
	r.mu.Unlock()
	if err := s.Action(context.Background(), "a", "stop"); err != nil {
		t.Fatal(err)
	}
	if err := s.Service("db").Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
}
