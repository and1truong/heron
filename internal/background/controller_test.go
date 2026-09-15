package background

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeBackend struct {
	process  ProcessState
	stop     func() error
	unloaded bool
}

func (f *fakeBackend) Status(context.Context) (ProcessState, error) { return f.process, nil }
func (f *fakeBackend) Start(context.Context, Spec) error            { return nil }
func (f *fakeBackend) Stop(context.Context) error {
	if f.stop != nil {
		return f.stop()
	}
	return nil
}
func (f *fakeBackend) Unload(context.Context) error { f.unloaded = true; return nil }

func fixture(t *testing.T) (Controller, *fakeBackend) {
	t.Helper()
	i := Instance{Dir: t.TempDir(), Config: "/missing.yaml"}
	if err := WriteJSON(i.SpecPath(), Spec{Token: "current"}); err != nil {
		t.Fatal(err)
	}
	f := &fakeBackend{}
	return Controller{Instance: i, Backend: f}, f
}

func TestStatusUsesManagerAndMatchingGeneration(t *testing.T) {
	for _, tc := range []struct {
		name    string
		process ProcessState
		record  RuntimeState
		want    string
	}{
		{"stopped", ProcessState{}, RuntimeState{}, "stopped"},
		{"ready", ProcessState{PID: 42, Active: true}, RuntimeState{Token: "current", PID: 42, State: "running"}, "running"},
		{"stale-generation", ProcessState{PID: 42, Active: true}, RuntimeState{Token: "old", PID: 42, State: "running"}, "starting"},
		{"pid-reused", ProcessState{PID: 99, Active: true}, RuntimeState{Token: "current", PID: 42, State: "running"}, "starting"},
		{"crashed", ProcessState{}, RuntimeState{Token: "current", PID: 42, State: "running"}, "failed"},
		{"manager-failed", ProcessState{Failed: true}, RuntimeState{}, "failed"},
		{"cleanup-in-progress", ProcessState{PID: 42, Active: true}, RuntimeState{Token: "current", PID: 42, State: "stopped"}, "stopping"},
		{"manager-stopping", ProcessState{PID: 42, Stopping: true}, RuntimeState{Token: "current", PID: 42, State: "running"}, "stopping"},
		{"graceful-exit", ProcessState{}, RuntimeState{Token: "current", PID: 42, State: "stopped"}, "stopped"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, f := fixture(t)
			f.process = tc.process
			if err := WriteJSON(c.Instance.StatePath(), tc.record); err != nil {
				t.Fatal(err)
			}
			s, err := c.Status(context.Background())
			if err != nil || s.State != tc.want {
				t.Fatalf("status=%+v err=%v, want %s", s, err, tc.want)
			}
		})
	}
}

func TestStopWaitsForExitWithoutConfig(t *testing.T) {
	c, f := fixture(t)
	f.process = ProcessState{Loaded: true, PID: 42, Active: true}
	f.stop = func() error {
		f.process = ProcessState{Loaded: true}
		return WriteJSON(c.Instance.StatePath(), RuntimeState{Token: "current", PID: 42, State: "stopped"})
	}
	if err := c.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !f.unloaded {
		t.Fatal("exited service not unloaded")
	}
}

func TestStopTimeoutDoesNotUnloadRunningProcess(t *testing.T) {
	c, f := fixture(t)
	f.process = ProcessState{Loaded: true, PID: 42, Active: true}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := c.Stop(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("stop error: %v", err)
	}
	if f.unloaded {
		t.Fatal("unloaded running process")
	}
}

func TestStopReportsRuntimeCleanupFailure(t *testing.T) {
	c, f := fixture(t)
	f.process = ProcessState{Loaded: true, PID: 42, Active: true}
	f.stop = func() error {
		f.process = ProcessState{Loaded: true, Failed: true}
		return WriteJSON(c.Instance.StatePath(), RuntimeState{Token: "current", PID: 42, State: "failed", Error: "custom stop failed"})
	}
	if err := c.Stop(context.Background()); err == nil || !strings.Contains(err.Error(), "custom stop failed") {
		t.Fatalf("stop error: %v", err)
	}
}

func TestWaitReadyRejectsStartupFailure(t *testing.T) {
	c, f := fixture(t)
	f.process = ProcessState{Failed: true}
	if err := WriteJSON(c.Instance.StatePath(), RuntimeState{Token: "current", State: "failed", Error: "port occupied"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.WaitReady(context.Background()); err == nil || !strings.Contains(err.Error(), "port occupied") {
		t.Fatalf("ready error: %v", err)
	}
}

func TestConfigIdentitySurvivesRemoval(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config with spaces.yaml")
	if err := os.WriteFile(path, []byte("apps: {}"), 0600); err != nil {
		t.Fatal(err)
	}
	before, err := Locate(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	after, err := Locate(path)
	if err != nil || before != after {
		t.Fatalf("identity changed: %+v, %+v, %v", before, after, err)
	}
}
