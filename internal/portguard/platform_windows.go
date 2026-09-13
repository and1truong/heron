//go:build windows

package portguard

import (
	"context"
	"encoding/csv"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

func inspectOwners(ctx context.Context, port int) ([]Owner, error) {
	cmd := exec.CommandContext(ctx, "netstat", "-ano", "-p", "tcp")
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	owners := parseNetstat(string(output), port)
	for i := range owners {
		owners[i].Command = windowsProcessName(ctx, owners[i].PID)
	}
	return owners, nil
}

func parseNetstat(output string, port int) []Owner {
	needle := ":" + strconv.Itoa(port)
	var owners []Owner
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 || !strings.EqualFold(fields[0], "TCP") || !strings.EqualFold(fields[3], "LISTENING") {
			continue
		}
		if !strings.HasSuffix(fields[1], needle) {
			continue
		}
		pid, err := strconv.Atoi(fields[4])
		if err == nil {
			owners = append(owners, Owner{PID: pid})
		}
	}
	return owners
}

func windowsProcessName(ctx context.Context, pid int) string {
	cmd := exec.CommandContext(ctx, "tasklist", "/FI", fmt.Sprintf("PID eq %d", pid), "/FO", "CSV", "/NH")
	output, err := cmd.Output()
	if err != nil {
		return ""
	}
	records, err := csv.NewReader(strings.NewReader(string(output))).ReadAll()
	if err != nil || len(records) == 0 || len(records[0]) == 0 {
		return ""
	}
	return records[0][0]
}

func terminatePID(pid int, force bool) error {
	args := []string{"/PID", strconv.Itoa(pid), "/T"}
	if force {
		args = append(args, "/F")
	}
	if output, err := exec.Command("taskkill", args...).CombinedOutput(); err != nil {
		return fmt.Errorf("taskkill: %w: %s", err, strings.TrimSpace(string(output)))
	}
	return nil
}
