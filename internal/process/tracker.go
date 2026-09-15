package process

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"
)

type Identity struct {
	PID       int
	StartTime string
}
type Stats struct {
	Identity
	PPID, Group int
	Command     string
	CPUTime     time.Duration
	CPU         float64
	RSS, VMS    uint64
}

// Tracker returns OS snapshots, independent of terminal rendering. Ownership is
// the process group created by Runner, anchored to the live launch process.
type Tracker interface {
	Snapshot(context.Context) ([]Stats, error)
}
type OSTracker struct{}

func (OSTracker) Snapshot(ctx context.Context) ([]Stats, error) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		return nil, fmt.Errorf("process metrics unsupported on %s", runtime.GOOS)
	}
	if runtime.GOOS == "linux" {
		// A container may expose the host's /proc inside a different PID
		// namespace. ps would return host IDs, which must never be compared to
		// the launch PID: collisions could attribute an unrelated host process.
		self, err := os.ReadFile("/proc/self/stat")
		if err != nil {
			return nil, fmt.Errorf("process metrics: read /proc/self/stat: %w", err)
		}
		if err := checkProcPID(string(self), os.Getpid()); err != nil {
			return nil, err
		}
	}
	cmd := exec.CommandContext(ctx, "ps", "-axo", "pid=,ppid=,pgid=,lstart=,time=,rss=,vsz=,comm=")
	cmd.Env = mergeEnvironment(cmd.Environ(), map[string]string{"LC_ALL": "C", "TZ": "UTC"})
	out, err := cmd.Output()
	if err != nil {
		if failure, ok := err.(*exec.ExitError); ok && len(failure.Stderr) > 0 {
			return nil, fmt.Errorf("process metrics: %w: %s", err, strings.TrimSpace(string(failure.Stderr)))
		}
		return nil, fmt.Errorf("process metrics: %w", err)
	}
	return parsePS(string(out)), nil
}

func checkProcPID(stat string, pid int) error {
	first, _, ok := strings.Cut(stat, " ")
	procPID, err := strconv.Atoi(first)
	if !ok || err != nil {
		return fmt.Errorf("process metrics: invalid /proc/self/stat")
	}
	if procPID != pid {
		return fmt.Errorf("process metrics: /proc PID namespace does not match; metrics unavailable")
	}
	return nil
}

func parsePS(text string) []Stats {
	var result []Stats
	for _, line := range strings.Split(text, "\n") {
		f := strings.Fields(line)
		if len(f) < 12 {
			continue
		}
		pid, e1 := strconv.Atoi(f[0])
		ppid, e2 := strconv.Atoi(f[1])
		group, e3 := strconv.Atoi(f[2])
		cpu, e4 := parseCPUTime(f[8])
		rss, e5 := strconv.ParseUint(f[9], 10, 64)
		vms, e6 := strconv.ParseUint(f[10], 10, 64)
		if e1 != nil || e2 != nil || e3 != nil || e4 != nil || e5 != nil || e6 != nil {
			continue
		}
		result = append(result, Stats{Identity: Identity{pid, strings.Join(f[3:8], " ")}, PPID: ppid, Group: group, CPUTime: cpu, RSS: rss * 1024, VMS: vms * 1024, Command: strings.Join(f[11:], " ")})
	}
	return result
}

func parseCPUTime(s string) (time.Duration, error) {
	days := 0.0
	if d, tail, ok := strings.Cut(s, "-"); ok {
		v, err := strconv.ParseFloat(d, 64)
		if err != nil {
			return 0, err
		}
		days = v
		s = tail
	}
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, fmt.Errorf("invalid CPU time %q", s)
	}
	seconds := 0.0
	for _, p := range parts {
		v, err := strconv.ParseFloat(p, 64)
		if err != nil {
			return 0, err
		}
		seconds = seconds*60 + v
	}
	return time.Duration((days*86400 + seconds) * float64(time.Second)), nil
}

type Sampler struct {
	at       time.Time
	previous map[Identity]time.Duration
}

func (s *Sampler) Sample(at time.Time, processes []Stats) []Stats {
	result := append([]Stats(nil), processes...)
	next := make(map[Identity]time.Duration, len(result))
	for i := range result {
		p := &result[i]
		p.CPU = 0
		if old, ok := s.previous[p.Identity]; ok && at.After(s.at) && p.CPUTime >= old {
			p.CPU = 100 * float64(p.CPUTime-old) / float64(at.Sub(s.at))
		}
		next[p.Identity] = p.CPUTime
	}
	s.previous = next
	s.at = at
	return result
}

// Owned never adopts an arbitrary group without a live group leader. Daemons
// that leave the launch group (e.g. docker compose) are intentionally unknown.
func Owned(pid int, processes []Stats) []Stats {
	if pid <= 0 {
		return nil
	}
	found := false
	for _, p := range processes {
		if p.PID == pid && p.Group == pid {
			found = true
			break
		}
	}
	if !found {
		return nil
	}
	var result []Stats
	for _, p := range processes {
		if p.Group == pid {
			result = append(result, p)
		}
	}
	return result
}
