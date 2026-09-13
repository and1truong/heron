package portguard

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strings"
	"time"
)

type Requirement struct {
	Port    int
	Purpose string
}

type Owner struct {
	PID     int
	Command string
}

type Conflict struct {
	Port     int
	Purposes []string
	Owners   []Owner
}

func Check(ctx context.Context, requirements []Requirement) ([]Conflict, error) {
	purposes := make(map[int]map[string]struct{})
	for _, requirement := range requirements {
		if requirement.Port < 1 || requirement.Port > 65535 {
			continue
		}
		if purposes[requirement.Port] == nil {
			purposes[requirement.Port] = make(map[string]struct{})
		}
		purposes[requirement.Port][requirement.Purpose] = struct{}{}
	}
	ports := make([]int, 0, len(purposes))
	for port := range purposes {
		ports = append(ports, port)
	}
	sort.Ints(ports)

	conflicts := make([]Conflict, 0)
	for _, port := range ports {
		free, err := portFree(port)
		if err != nil {
			return nil, fmt.Errorf("check port %d: %w", port, err)
		}
		if free {
			continue
		}
		owners, _ := inspectOwners(ctx, port)
		purposeList := make([]string, 0, len(purposes[port]))
		for purpose := range purposes[port] {
			purposeList = append(purposeList, purpose)
		}
		sort.Strings(purposeList)
		conflicts = append(conflicts, Conflict{Port: port, Purposes: purposeList, Owners: dedupeOwners(owners)})
	}
	return conflicts, nil
}

func Release(ctx context.Context, conflicts []Conflict, gracefulTimeout time.Duration) error {
	pids := make(map[int]struct{})
	ports := make([]int, 0, len(conflicts))
	for _, conflict := range conflicts {
		ports = append(ports, conflict.Port)
		if len(conflict.Owners) == 0 {
			return fmt.Errorf("cannot release port %d: owning process could not be identified", conflict.Port)
		}
		for _, owner := range conflict.Owners {
			if owner.PID > 0 {
				pids[owner.PID] = struct{}{}
			}
		}
	}
	for pid := range pids {
		if err := terminatePID(pid, false); err != nil {
			return fmt.Errorf("terminate PID %d: %w", pid, err)
		}
	}
	if waitFree(ctx, ports, gracefulTimeout) == nil {
		return nil
	}
	for pid := range pids {
		if err := terminatePID(pid, true); err != nil {
			return fmt.Errorf("force terminate PID %d: %w", pid, err)
		}
	}
	if err := waitFree(ctx, ports, 2*time.Second); err != nil {
		return err
	}
	return nil
}

func waitFree(ctx context.Context, ports []int, timeout time.Duration) error {
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		allFree := true
		for _, port := range ports {
			free, err := portFree(port)
			if err != nil {
				return fmt.Errorf("verify port %d: %w", port, err)
			}
			if !free {
				allFree = false
				break
			}
		}
		if allFree {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline.C:
			return fmt.Errorf("ports still occupied after termination: %v", ports)
		case <-tick.C:
		}
	}
}

func portFree(port int) (bool, error) {
	listener, err := net.Listen("tcp4", net.JoinHostPort("127.0.0.1", fmt.Sprint(port)))
	if err == nil {
		_ = listener.Close()
		return true, nil
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return false, nil
	}
	return false, err
}

func dedupeOwners(owners []Owner) []Owner {
	byPID := make(map[int]Owner)
	for _, owner := range owners {
		if owner.PID <= 0 {
			continue
		}
		if prior, ok := byPID[owner.PID]; ok && prior.Command != "" {
			continue
		}
		byPID[owner.PID] = owner
	}
	pids := make([]int, 0, len(byPID))
	for pid := range byPID {
		pids = append(pids, pid)
	}
	sort.Ints(pids)
	result := make([]Owner, 0, len(pids))
	for _, pid := range pids {
		owner := byPID[pid]
		owner.Command = strings.TrimSpace(owner.Command)
		result = append(result, owner)
	}
	return result
}
