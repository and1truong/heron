package background

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"
)

type Status struct {
	State     string
	PID       int
	URL       string
	StartedAt time.Time
	Error     string
}

type Controller struct {
	Instance Instance
	Backend  Backend
}

func (c Controller) Status(ctx context.Context) (Status, error) {
	process, err := c.Backend.Status(ctx)
	if err != nil {
		return Status{}, err
	}
	var spec Spec
	if err := ReadJSON(c.Instance.SpecPath(), &spec); err != nil && !errors.Is(err, os.ErrNotExist) {
		return Status{}, err
	}
	var record RuntimeState
	err = ReadJSON(c.Instance.StatePath(), &record)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Status{}, err
	}
	valid := err == nil && record.Token != "" && record.Token == spec.Token
	s := Status{State: "stopped", PID: process.PID}
	if valid {
		s.URL, s.StartedAt, s.Error = record.URL, record.StartedAt, record.Error
	}
	switch {
	case process.Stopping:
		s.State = "stopping"
	case process.PID != 0 || process.Active:
		s.State = "starting"
		if valid && record.PID == process.PID {
			s.State = record.State
			// A completed runtime is still stopping until the manager sees exit.
			if s.State == "stopped" {
				s.State = "stopping"
			}
		}
	case process.Failed || valid && (record.State == "failed" || record.State == "running" || record.State == "starting" || record.State == "stopping"):
		s.State = "failed"
		if s.Error == "" {
			s.Error = "service exited before completing graceful shutdown; inspect service.log and the user service manager"
		}
	}
	return s, nil
}

func waitTick(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(100 * time.Millisecond):
		return nil
	}
}

func (c Controller) WaitReady(ctx context.Context) (Status, error) {
	for {
		s, err := c.Status(ctx)
		if err != nil {
			return s, err
		}
		if s.State == "running" {
			return s, nil
		}
		if s.State == "failed" {
			return s, fmt.Errorf("service startup failed: %s (log: %s)", s.Error, c.Instance.LogPath())
		}
		if err := waitTick(ctx); err != nil {
			return s, fmt.Errorf("waiting for service readiness: %w (log: %s)", err, c.Instance.LogPath())
		}
	}
}

// Stop uses the service manager identity, never a PID read from disk.
func (c Controller) Stop(ctx context.Context) error {
	p, err := c.Backend.Status(ctx)
	if err != nil {
		return err
	}
	wasRunning := p.PID != 0 || p.Active || p.Stopping
	if wasRunning && !p.Stopping {
		if err := c.Backend.Stop(ctx); err != nil {
			// The process may have exited between status and the stop request.
			after, queryErr := c.Backend.Status(ctx)
			if queryErr != nil || after.PID != 0 || after.Active || after.Stopping {
				return err
			}
		}
	}
	for {
		p, err = c.Backend.Status(ctx)
		if err != nil {
			return err
		}
		if p.PID == 0 && !p.Active && !p.Stopping {
			break
		}
		if err := waitTick(ctx); err != nil {
			return fmt.Errorf("service is still stopping: %w; no replacement started and no forced kill attempted", err)
		}
	}
	s, err := c.Status(ctx)
	if err != nil {
		return err
	}
	if p.Loaded {
		if err := c.Backend.Unload(ctx); err != nil {
			return err
		}
	}
	if wasRunning && s.State == "failed" {
		return fmt.Errorf("service shutdown failed: %s (log: %s)", s.Error, c.Instance.LogPath())
	}
	return nil
}
