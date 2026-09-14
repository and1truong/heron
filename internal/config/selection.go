package config

import (
	"fmt"
	"sort"
	"strings"
)

// SelectApp returns an isolated runtime containing an app and its transitive
// dependencies. Global lifecycle hooks remain; global scheduled jobs are disabled.
func (c RuntimeConfig) SelectApp(id string) (RuntimeConfig, error) {
	if _, ok := c.Apps[id]; !ok {
		ids := make([]string, 0, len(c.Apps))
		for name := range c.Apps {
			ids = append(ids, name)
		}
		sort.Strings(ids)
		return RuntimeConfig{}, fmt.Errorf("unknown app %q (available: %s)", id, strings.Join(ids, ", "))
	}
	selected := make(map[string]RuntimeAppConfig)
	var visit func(string) error
	visit = func(name string) error {
		if _, ok := selected[name]; ok {
			return nil
		}
		app, ok := c.Apps[name]
		if !ok {
			return fmt.Errorf("unknown dependency %q", name)
		}
		selected[name] = app
		for _, dep := range app.DependsOn {
			if err := visit(dep); err != nil {
				return err
			}
		}
		return nil
	}
	if err := visit(id); err != nil {
		return RuntimeConfig{}, err
	}
	c.Apps = selected
	c.ScheduledTasks = nil
	if _, err := c.DependencyOrder(); err != nil {
		return RuntimeConfig{}, err
	}
	return c, nil
}
