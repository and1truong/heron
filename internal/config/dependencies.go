package config

import (
	"fmt"
	"sort"
	"strings"
)

// DependencyOrder validates the graph and returns dependencies before dependents.
func (c RuntimeConfig) DependencyOrder() ([]string, error) {
	marks := map[string]int{}
	var order, chain []string
	var visit func(string) error
	visit = func(id string) error {
		if marks[id] == 2 {
			return nil
		}
		if marks[id] == 1 {
			return fmt.Errorf("dependency cycle: %s", strings.Join(append(chain, id), " -> "))
		}
		app, ok := c.Apps[id]
		if !ok {
			return fmt.Errorf("unknown dependency: %s", strings.Join(append(chain, id), " -> "))
		}
		marks[id] = 1
		chain = append(chain, id)
		seen := map[string]bool{}
		for _, dep := range app.DependsOn {
			if seen[dep] {
				return fmt.Errorf("app %q: duplicate dependency %q", id, dep)
			}
			seen[dep] = true
			if err := visit(dep); err != nil {
				return err
			}
		}
		chain = chain[:len(chain)-1]
		marks[id] = 2
		order = append(order, id)
		return nil
	}
	ids := make([]string, 0, len(c.Apps))
	for id := range c.Apps {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if err := visit(id); err != nil {
			return nil, err
		}
	}
	return order, nil
}
