package scheduler

import (
	"context"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/and1truong/heron/internal/config"
	proc "github.com/and1truong/heron/internal/process"
)

// Scheduler runs process-wide commands on fixed intervals while Heron is active.
type Scheduler struct {
	tasks  map[string]config.RuntimeScheduledTaskConfig
	runner proc.ProcessRunner
	logger *slog.Logger

	startOnce sync.Once
	stopOnce  sync.Once
	cancel    context.CancelFunc
	loops     sync.WaitGroup
	runs      sync.WaitGroup
}

func New(tasks map[string]config.RuntimeScheduledTaskConfig, runner proc.ProcessRunner, logger *slog.Logger) *Scheduler {
	copied := make(map[string]config.RuntimeScheduledTaskConfig, len(tasks))
	for name, task := range tasks {
		copied[name] = task
	}
	return &Scheduler{tasks: copied, runner: runner, logger: logger}
}

func (s *Scheduler) Start(parent context.Context) {
	s.startOnce.Do(func() {
		ctx, cancel := context.WithCancel(parent)
		s.cancel = cancel
		names := make([]string, 0, len(s.tasks))
		for name := range s.tasks {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			task := s.tasks[name]
			s.loops.Add(1)
			go s.loop(ctx, task)
		}
	})
}

// Stop prevents new runs, cancels active commands, and waits for them to exit.
func (s *Scheduler) Stop() {
	s.stopOnce.Do(func() {
		if s.cancel != nil {
			s.cancel()
		}
		s.loops.Wait()
		s.runs.Wait()
	})
}

func (s *Scheduler) loop(ctx context.Context, task config.RuntimeScheduledTaskConfig) {
	defer s.loops.Done()
	ticker := time.NewTicker(task.Every)
	defer ticker.Stop()

	done := make(chan time.Time, 1)
	running := false
	var lastCompletion time.Time
	run := func() {
		if running {
			if s.logger != nil {
				s.logger.Warn("scheduled task skipped because previous run is active", "task", task.Name)
			}
			return
		}
		running = true
		s.runs.Add(1)
		go func() {
			defer s.runs.Done()
			s.run(ctx, task)
			done <- time.Now()
		}()
	}
	if task.RunOnStart {
		run()
	}
	for {
		select {
		case <-ctx.Done():
			return
		case tick := <-ticker.C:
			// A tick buffered while the previous invocation was running is
			// still a skipped tick, even if completion wins the select first.
			if !lastCompletion.IsZero() && tick.Before(lastCompletion) {
				continue
			}
			run()
		case completed := <-done:
			running = false
			lastCompletion = completed
		}
	}
}

func (s *Scheduler) run(parent context.Context, task config.RuntimeScheduledTaskConfig) {
	ctx, cancel := context.WithTimeout(parent, task.Timeout)
	defer cancel()
	if s.logger != nil {
		s.logger.Info("scheduled task started", "task", task.Name)
	}
	err := s.runner.Run(ctx, proc.CommandSpec{
		Command: task.Command,
		Service: "heron",
		Kind:    "scheduledTask:" + task.Name,
	})
	if err == nil {
		if s.logger != nil {
			s.logger.Info("scheduled task completed", "task", task.Name)
		}
		return
	}
	if parent.Err() != nil {
		return
	}
	if s.logger != nil {
		s.logger.Warn("scheduled task failed", "task", task.Name, "err", err)
	}
}
