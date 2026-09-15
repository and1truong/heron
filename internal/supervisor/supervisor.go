package supervisor

import (
	"context"
	"errors"
	"github.com/and1truong/heron/internal/config"
	proc "github.com/and1truong/heron/internal/process"
	"log/slog"
	"sync"
	"sync/atomic"
)

type Supervisor struct {
	actionMu   sync.Mutex
	loadConfig func() (config.RuntimeConfig, error)
	services   map[string]*Service
	logger     *slog.Logger
	closing    atomic.Bool
	cancel     context.CancelFunc
	order      []string
}

func New(c config.RuntimeConfig, r proc.ProcessRunner, l *slog.Logger) *Supervisor {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Supervisor{services: map[string]*Service{}, logger: l, cancel: cancel}
	for id, a := range c.Apps {
		s.services[id] = newService(ctx, a, r, l)
	}
	s.order, _ = c.DependencyOrder()
	for id, a := range c.Apps {
		for _, dep := range a.DependsOn {
			s.services[id].dependencies = append(s.services[id].dependencies, s.services[dep])
		}
	}
	return s
}
func (s *Supervisor) Service(id string) *Service { return s.services[id] }
func (s *Supervisor) Acquire(ctx context.Context, id string) (func(), error) {
	if s.closing.Load() {
		return nil, ErrClosing
	}
	v := s.services[id]
	if v == nil {
		return nil, errors.New("unknown service")
	}
	return v.Acquire(ctx)
}
func (s *Supervisor) StopAll(ctx context.Context) error {
	s.closing.Store(true)
	// Block new acquisitions without cancelling running processes out of order.
	for _, v := range s.services {
		v.mu.Lock()
		v.manualStopped = true
		if (v.state == StateBuilding || v.state == StateStarting) && v.startCancel != nil {
			v.startCancel()
		}
		v.mu.Unlock()
	}
	// Cancel in-flight startup before waiting for a manual action to finish.
	s.actionMu.Lock()
	defer s.actionMu.Unlock()
	var errs []error
	for i := len(s.order) - 1; i >= 0; i-- {
		if err := s.services[s.order[i]].Stop(ctx); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) == 0 {
		s.cancel()
	}
	return errors.Join(errs...)
}
