package supervisor

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/and1truong/heron/internal/config"
	proc "github.com/and1truong/heron/internal/process"
)

type retryRunner struct {
	builds    atomic.Int32
	starts    atomic.Int32
	failBuild bool
	failure   error
	done      chan struct{}
}

func (r *retryRunner) Run(context.Context, proc.CommandSpec) error {
	if r.builds.Add(1) == 1 && r.failBuild {
		return r.failure
	}
	return nil
}

func (r *retryRunner) Start(context.Context, proc.CommandSpec) (*proc.Process, error) {
	if r.starts.Add(1) == 1 && !r.failBuild {
		return nil, r.failure
	}
	return &proc.Process{Done: r.done}, nil
}

func newRetryService(t *testing.T, failBuild bool) (*Service, *retryRunner) {
	t.Helper()
	r := &retryRunner{failBuild: failBuild, failure: errors.New("temporary startup failure"), done: make(chan struct{})}
	t.Cleanup(func() { close(r.done) })
	s := newService(context.Background(), config.RuntimeAppConfig{
		ID: "api", Build: "build", Launch: "launch", StartTimeout: time.Second,
	}, r, slog.New(slog.NewTextHandler(io.Discard, nil)))
	return s, r
}

func TestNextRequestRetriesFailedStartup(t *testing.T) {
	for _, failBuild := range []bool{true, false} {
		name := "launch"
		if failBuild {
			name = "build"
		}
		t.Run(name, func(t *testing.T) {
			s, r := newRetryService(t, failBuild)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			if _, err := s.Acquire(ctx); !errors.Is(err, r.failure) {
				t.Fatalf("first request error = %v, want %v", err, r.failure)
			}
			if s.State() != StateFailed {
				t.Fatal("failed startup did not enter failed state")
			}
			release, err := s.Acquire(ctx)
			if err != nil {
				t.Fatalf("retry: %v", err)
			}
			defer release()
			if s.State() != StateRunning || r.builds.Load() != 2 {
				t.Fatalf("state=%v builds=%d", s.State(), r.builds.Load())
			}
			wantStarts := int32(2)
			if failBuild {
				wantStarts = 1
			}
			if r.starts.Load() != wantStarts {
				t.Fatalf("starts=%d, want %d", r.starts.Load(), wantStarts)
			}
		})
	}
}

// Pause a caller at the wait boundary, after it has captured its startup
// attempt. Another request can then finish a retry before this caller resumes.
type pausedWaitContext struct {
	context.Context
	entered chan struct{}
	resume  chan struct{}
}

func (c *pausedWaitContext) Done() <-chan struct{} {
	close(c.entered)
	select {
	case <-c.resume:
	case <-c.Context.Done():
	}
	return c.Context.Done()
}

func TestRetryPreservesPreviousWaiterFailure(t *testing.T) {
	s, r := newRetryService(t, true)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	paused := &pausedWaitContext{Context: ctx, entered: make(chan struct{}), resume: make(chan struct{})}
	result := make(chan error, 1)
	go func() {
		release, err := s.Acquire(paused)
		if release != nil {
			release()
		}
		result <- err
	}()
	select {
	case <-paused.entered:
	case <-ctx.Done():
		t.Fatal("first request never reached startup wait")
	}
	s.mu.Lock()
	attemptDone := s.workerDone
	s.mu.Unlock()
	if err := wait(ctx, attemptDone); err != nil {
		t.Fatal(err)
	}
	release, err := s.Acquire(ctx)
	if err != nil {
		t.Fatalf("next request should retry: %v", err)
	}
	defer release()
	close(paused.resume)
	select {
	case err := <-result:
		if !errors.Is(err, r.failure) {
			t.Fatalf("original waiter error = %v, want original failure %v", err, r.failure)
		}
	case <-ctx.Done():
		t.Fatal("original waiter did not finish")
	}
}
