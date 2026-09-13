package config

import (
	"reflect"
	"strings"
	"testing"
)

func TestDependencyValidation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		graph map[string][]string
		want  string
	}{
		{"unknown", map[string][]string{"a": {"missing"}}, "a -> missing"},
		{"self", map[string][]string{"a": {"a"}}, "a -> a"},
		{"cycle", map[string][]string{"a": {"b"}, "b": {"c"}, "c": {"a"}}, "a -> b -> c -> a"},
		{"duplicate", map[string][]string{"a": {"b", "b"}, "b": nil}, "duplicate dependency"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := Config{Apps: map[string]AppConfig{}}
			pwd := t.TempDir()
			for id, deps := range tc.graph {
				c.Apps[id] = AppConfig{Pwd: pwd, Launch: "launch", DependsOn: deps}
			}
			_, err := c.Normalize()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v; want %s", err, tc.want)
			}
		})
	}
}
func TestNormalizedDependencies(t *testing.T) {
	pwd := t.TempDir()
	c := Config{Apps: map[string]AppConfig{"a": {Pwd: pwd, Launch: "a", DependsOn: []string{"b"}}, "b": {Pwd: pwd, Launch: "b"}}}
	r, err := c.Normalize()
	if err != nil {
		t.Fatal(err)
	}
	order, err := r.DependencyOrder()
	if err != nil || !reflect.DeepEqual(order, []string{"b", "a"}) {
		t.Fatalf("%v %v", order, err)
	}
	c.Apps["a"].DependsOn[0] = "changed"
	if r.Apps["a"].DependsOn[0] != "b" {
		t.Fatal("dependency slice aliases input")
	}
}
