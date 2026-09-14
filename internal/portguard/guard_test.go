package portguard

import (
	"context"
	"net"
	"testing"
)

func TestCheckFindsOccupiedAndFreePorts(t *testing.T) {
	occupied, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer occupied.Close()
	occupiedPort := occupied.Addr().(*net.TCPAddr).Port

	freeProbe, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	freePort := freeProbe.Addr().(*net.TCPAddr).Port
	_ = freeProbe.Close()

	conflicts, err := Check(context.Background(), []Requirement{
		{Port: occupiedPort, Purpose: "backend"},
		{Port: occupiedPort, Purpose: "duplicate purpose"},
		{Port: freePort, Purpose: "free"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(conflicts) != 1 || conflicts[0].Port != occupiedPort {
		t.Fatalf("conflicts = %#v", conflicts)
	}
	if len(conflicts[0].Purposes) != 2 {
		t.Fatalf("purposes = %#v", conflicts[0].Purposes)
	}
}

func TestDedupeOwnersPrefersNamedOwner(t *testing.T) {
	owners := dedupeOwners([]Owner{{PID: 7}, {PID: 3, Command: "first"}, {PID: 7, Command: "named"}, {PID: 3, Command: "ignored"}})
	if len(owners) != 2 || owners[0].PID != 3 || owners[1].PID != 7 || owners[1].Command != "named" {
		t.Fatalf("owners = %#v", owners)
	}
}
