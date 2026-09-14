package scheduler

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/and1truong/heron/internal/config"
	proc "github.com/and1truong/heron/internal/process"
)

type fakeRunner struct {
	mu      sync.Mutex
	calls   []proc.CommandSpec
	started chan struct{}
	release chan struct{}
	fail    error
}

func (r *fakeRunner) Run(ctx context.Context, spec proc.CommandSpec) error {
	r.mu.Lock()
	r.calls = append(r.calls, spec)
	r.mu.Unlock()
	if r.started != nil {
		select {
		case r.started <- struct{}{}:
		default:
		}
	}
	if r.release != nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-r.release:
		}
	}
	return r.fail
}

func (r *fakeRunner) Start(context.Context, proc.CommandSpec) (*proc.Process, error) {
	panic("unexpected Start call")
}

func (r *fakeRunner) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func TestRunOnStartUsesTaskIdentity(t *testing.T) {
	runner := &fakeRunner{started: make(chan struct{}, 1)}
	s := New(map[string]config.RuntimeScheduledTaskConfig{
		"cleanup": {Name: "cleanup", Command: "./cleanup.sh", Every: time.Hour, Timeout: time.Minute, RunOnStart: true, Overlap: "skip"},
	}, runner, nil)
	s.Start(context.Background())
	await(t, runner.started)
	s.Stop()

	runner.mu.Lock()
	defer runner.mu.Unlock()
	if len(runner.calls) != 1 || runner.calls[0].Command != "./cleanup.sh" ||
		runner.calls[0].Service != "heron" || runner.calls[0].Kind != "scheduledTask:cleanup" {
		t.Fatalf("calls = %#v", runner.calls)
	}
}

func TestSlowRunSkipsOverlappingTicks(t *testing.T) {
	runner := &fakeRunner{started: make(chan struct{}, 2), release: make(chan struct{})}
	s := New(map[string]config.RuntimeScheduledTaskConfig{
		"slow": {Name: "slow", Command: "slow", Every: 10 * time.Millisecond, Timeout: time.Second, RunOnStart: true, Overlap: "skip"},
	}, runner, nil)
	s.Start(context.Background())
	await(t, runner.started)
	time.Sleep(45 * time.Millisecond)
	if got := runner.count(); got != 1 {
		t.Fatalf("runs = %d, want 1 while the first run is active", got)
	}
	s.Stop()
}

func TestFailureDoesNotStopLaterRuns(t *testing.T) {
	runner := &fakeRunner{started: make(chan struct{}, 4), fail: errors.New("boom")}
	s := New(map[string]config.RuntimeScheduledTaskConfig{
		"retry": {Name: "retry", Command: "fails", Every: 10 * time.Millisecond, Timeout: time.Second, Overlap: "skip"},
	}, runner, nil)
	s.Start(context.Background())
	await(t, runner.started)
	await(t, runner.started)
	s.Stop()
	if got := runner.count(); got < 2 {
		t.Fatalf("runs = %d, want at least 2", got)
	}
}

func TestStopCancelsAndJoinsActiveRun(t *testing.T) {
	runner := &fakeRunner{started: make(chan struct{}, 1), release: make(chan struct{})}
	s := New(map[string]config.RuntimeScheduledTaskConfig{
		"blocked": {Name: "blocked", Command: "blocked", Every: time.Hour, Timeout: time.Hour, RunOnStart: true, Overlap: "skip"},
	}, runner, nil)
	s.Start(context.Background())
	await(t, runner.started)

	done := make(chan struct{})
	go func() {
		s.Stop()
		close(done)
	}()
	await(t, done)
}

func await(t *testing.T, ch <-chan struct{}) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for scheduler")
	}
}
