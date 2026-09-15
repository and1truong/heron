package process

import (
	"testing"
	"time"
)

func TestPSAndOwnedGroup(t *testing.T) {
	ps := parsePS("10 1 10 Sun Sep 13 07:00:00 2026 01:02 100 200 /bin/sh\n11 1 10 Sun Sep 13 07:00:01 2026 00:00.50 20 40 worker\n12 10 12 Sun Sep 13 07:00:01 2026 00:00 10 20 detached\nbroken\n")
	if len(ps) != 3 || ps[0].CPUTime != 62*time.Second || ps[0].RSS != 102400 {
		t.Fatalf("parsed: %+v", ps)
	}
	owned := Owned(10, ps)
	if len(owned) != 2 || owned[1].PID != 11 {
		t.Fatalf("group ownership should include reparented member, exclude detached: %+v", owned)
	}
	if len(Owned(10, ps[1:])) != 0 {
		t.Fatal("adopted group without live leader")
	}
}

func TestCPUDeltasAndPIDReuse(t *testing.T) {
	s := Sampler{}
	at := time.Unix(100, 0)
	p := Stats{Identity: Identity{10, "first"}, CPUTime: time.Second}
	if s.Sample(at, []Stats{p})[0].CPU != 0 {
		t.Fatal("first sample must establish baseline")
	}
	p.CPUTime += 2400 * time.Millisecond
	if cpu := s.Sample(at.Add(time.Second), []Stats{p})[0].CPU; cpu != 240 {
		t.Fatalf("CPU=%v", cpu)
	}
	p.StartTime = "reused"
	p.CPUTime = 20 * time.Second
	if s.Sample(at.Add(2*time.Second), []Stats{p})[0].CPU != 0 {
		t.Fatal("PID reuse inherited CPU baseline")
	}
	s.Sample(at.Add(3*time.Second), nil)
	if len(s.previous) != 0 {
		t.Fatal("dead process retained")
	}
}

func TestCPUTimeFormats(t *testing.T) {
	for input, want := range map[string]time.Duration{"01:02": 62 * time.Second, "00:00.50": 500 * time.Millisecond, "1-02:03:04": 93784 * time.Second} {
		got, err := parseCPUTime(input)
		if err != nil || got != want {
			t.Fatalf("%s = %s, %v", input, got, err)
		}
	}
}

func TestProcNamespaceMismatchCannotAttributeHostProcesses(t *testing.T) {
	if err := checkProcPID("42 (heron) R 1 42 42", 42); err != nil {
		t.Fatal(err)
	}
	if err := checkProcPID("10042 (heron) R 1 10042 10042", 42); err == nil {
		t.Fatal("accepted host PIDs in a nested PID namespace")
	}
	if err := checkProcPID("malformed", 42); err == nil {
		t.Fatal("accepted malformed process identity")
	}
}
