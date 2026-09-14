package config

import (
	"reflect"
	"strings"
	"testing"
)

func TestSelectApp(t *testing.T) {
	cfg := RuntimeConfig{Port: 8080, StartUp: []string{"setup"}, TearDown: []string{"cleanup"}, ScheduledTasks: map[string]RuntimeScheduledTaskConfig{"job": {}}, Apps: map[string]RuntimeAppConfig{
		"api":   {DependsOn: []string{"cache", "db"}},
		"cache": {DependsOn: []string{"db"}},
		"db":    {}, "unrelated": {},
	}}
	for _, tc := range []struct {
		id   string
		want []string
	}{{"api", []string{"db", "cache", "api"}}, {"db", []string{"db"}}} {
		selected, err := cfg.SelectApp(tc.id)
		if err != nil {
			t.Fatal(err)
		}
		order, err := selected.DependencyOrder()
		if err != nil || !reflect.DeepEqual(order, tc.want) {
			t.Fatalf("order = %v, err = %v", order, err)
		}
		if len(selected.Apps) != len(tc.want) || len(selected.ScheduledTasks) != 0 {
			t.Fatalf("not isolated: %+v", selected)
		}
		if selected.Port != cfg.Port || !reflect.DeepEqual(selected.StartUp, cfg.StartUp) || !reflect.DeepEqual(selected.TearDown, cfg.TearDown) {
			t.Fatal("global settings lost")
		}
	}
	if len(cfg.Apps) != 4 || len(cfg.ScheduledTasks) != 1 {
		t.Fatal("input mutated")
	}
	for _, id := range []string{"", "missing", "API"} {
		if _, err := cfg.SelectApp(id); err == nil || !strings.Contains(err.Error(), "available: api, cache, db, unrelated") {
			t.Fatalf("error = %v", err)
		}
	}
}

func TestSelectAppRejectsInvalidDependencyGraph(t *testing.T) {
	for _, apps := range []map[string]RuntimeAppConfig{
		{"api": {DependsOn: []string{"missing"}}},
		{"api": {DependsOn: []string{"db"}}, "db": {DependsOn: []string{"api"}}},
	} {
		if _, err := (RuntimeConfig{Apps: apps}).SelectApp("api"); err == nil {
			t.Fatal("expected graph error")
		}
	}
}
