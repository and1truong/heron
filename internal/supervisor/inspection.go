package supervisor

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"time"

	"github.com/and1truong/heron/internal/config"
)

type Snapshot struct {
	DependsOn        []string
	ActiveDependents []string
	ID               string
	State            State
	PID              int
	StartedAt        time.Time
	Restarts         int
	ManualStopped    bool
}

func (s State) String() string {
	switch s {
	case StateStopped:
		return "stopped"
	case StateBuilding:
		return "building"
	case StateStarting:
		return "starting"
	case StateRunning:
		return "running"
	case StateStopping:
		return "stopping"
	case StateFailed:
		return "failed"
	default:
		return "unknown"
	}
}

func (s *Supervisor) Snapshots() []Snapshot {
	result := make([]Snapshot, 0, len(s.services))
	for id, v := range s.services {
		v.mu.Lock()
		x := Snapshot{DependsOn: append([]string(nil), v.cfg.DependsOn...), ActiveDependents: v.dependentIDsLocked(), ID: id, State: v.state, StartedAt: v.startedAt, Restarts: max(0, v.starts-1), ManualStopped: v.manualStopped}
		if p := v.process; p != nil && p.Cmd != nil && p.Cmd.Process != nil {
			select {
			case <-p.Done:
			default:
				x.PID = p.Cmd.Process.Pid
			}
		}
		v.mu.Unlock()
		result = append(result, x)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

// Action uses the same lifecycle as proxy requests. Manual stop inhibits lazy
// restarts until Start/Restart, so incoming traffic cannot undo a user's stop.
func (s *Supervisor) Action(ctx context.Context, id, action string) error {
	s.actionMu.Lock()
	defer s.actionMu.Unlock()
	if s.closing.Load() {
		return ErrClosing
	}
	v := s.services[id]
	if v == nil {
		return ErrUnknownService
	}
	var replacement *config.RuntimeAppConfig
	if action == "restart" && s.loadConfig != nil {
		cfg, err := s.loadConfig()
		if err != nil {
			return fmt.Errorf("reload configuration: %w", err)
		}
		fresh, ok := cfg.Apps[id]
		if !ok {
			return fmt.Errorf("service %q removed from configuration; full restart required", id)
		}
		old := v.Config()
		if !reflect.DeepEqual(old.EndpointList(), fresh.EndpointList()) || !reflect.DeepEqual(old.DependsOn, fresh.DependsOn) {
			return errors.New("endpoint or dependency changes require a full Heron restart")
		}
		replacement = &fresh
	}
	switch action {
	case "stop", "kill", "restart":
		v.mu.Lock()
		if err := v.dependencyStopErrorLocked(); err != nil {
			v.mu.Unlock()
			return err
		}
		var killErr error
		if action == "kill" {
			if v.cfg.Stop != "" {
				v.mu.Unlock()
				return errors.New("force kill unavailable for externally managed apps; use stop")
			}
			if v.process != nil {
				killErr = v.process.Kill()
			}
		}
		v.manualStopped = true
		v.mu.Unlock()
		if err := v.Stop(ctx); err != nil {
			return err
		}
		if killErr != nil {
			return killErr
		}
		if action != "restart" {
			return nil
		}
	case "start":
	default:
		return errors.New("unknown action")
	}
	v.mu.Lock()
	if replacement != nil {
		v.cfg = *replacement
	}
	v.manualStopped = false
	v.mu.Unlock()
	release, err := s.Acquire(ctx, id)
	if err == nil {
		release()
	}
	return err
}

var ErrUnknownService = errors.New("unknown service")

// SetConfigLoader must be called before serving requests. Every manual restart
// validates fresh config before stopping, then installs only the target app.
func (s *Supervisor) SetConfigLoader(load func() (config.RuntimeConfig, error)) {
	s.loadConfig = load
}
