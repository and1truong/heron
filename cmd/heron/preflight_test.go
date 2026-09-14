package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/and1truong/heron/internal/config"
	"github.com/and1truong/heron/internal/portguard"
)

func TestPortRequirementsIncludeWrapperBackendsAndTCPListeners(t *testing.T) {
	cfg := config.RuntimeConfig{
		Port: 3000,
		Apps: map[string]config.RuntimeAppConfig{
			"api": {
				ID: "api",
				Endpoints: map[string]config.RuntimeEndpointConfig{
					"web": {Name: "web", Protocol: config.ProtocolHTTP, Port: 8080, Primary: true},
					"db":  {Name: "db", Protocol: config.ProtocolTCP, Port: 5432, ListenPort: 15432},
				},
			},
		},
	}
	requirements := portRequirements(cfg)
	got := make(map[int]string)
	for _, requirement := range requirements {
		got[requirement.Port] = requirement.Purpose
	}
	for _, port := range []int{3000, 8080, 5432, 15432} {
		if got[port] == "" {
			t.Fatalf("port %d missing from %#v", port, requirements)
		}
	}
}

func TestPrintPortConflictsShowsProcessDetails(t *testing.T) {
	var output bytes.Buffer
	printPortConflicts(&output, []portguard.Conflict{{
		Port: 3000, Purposes: []string{"Heron listener"}, Owners: []portguard.Owner{{PID: 42, Command: "bun run dev"}},
	}})
	text := output.String()
	for _, want := range []string{"port 3000", "PID 42", "bun run dev", "Heron listener"} {
		if !strings.Contains(text, want) {
			t.Fatalf("output missing %q: %s", want, text)
		}
	}
}
