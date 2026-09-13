//go:build linux || darwin

package portguard

import "testing"

func TestParseLsof(t *testing.T) {
	owners := parseLsof("p48122\ncbun\np912\ncpostgres\n")
	if len(owners) != 2 || owners[0].PID != 48122 || owners[0].Command != "bun" || owners[1].PID != 912 || owners[1].Command != "postgres" {
		t.Fatalf("owners = %#v", owners)
	}
}

func TestParseSS(t *testing.T) {
	output := `LISTEN 0 128 127.0.0.1:3000 0.0.0.0:* users:(("bun",pid=48122,fd=12))
LISTEN 0 128 127.0.0.1:4000 0.0.0.0:* users:(("other",pid=99,fd=3))`
	owners := parseSS(output, 3000)
	if len(owners) != 1 || owners[0].PID != 48122 || owners[0].Command != "bun" {
		t.Fatalf("owners = %#v", owners)
	}
}
