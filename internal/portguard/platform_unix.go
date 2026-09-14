//go:build linux || darwin

package portguard

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"syscall"
)

var ssProcessPattern = regexp.MustCompile(`\(\("([^\"]+)",pid=([0-9]+),`)

func inspectOwners(ctx context.Context, port int) ([]Owner, error) {
	if owners, err := inspectWithLsof(ctx, port); err == nil && len(owners) > 0 {
		return owners, nil
	}
	if owners, err := inspectWithSS(ctx, port); err == nil && len(owners) > 0 {
		return owners, nil
	}
	return nil, fmt.Errorf("no supported process inspector found for port %d", port)
}

func inspectWithLsof(ctx context.Context, port int) ([]Owner, error) {
	cmd := exec.CommandContext(ctx, "lsof", "-nP", "-iTCP:"+strconv.Itoa(port), "-sTCP:LISTEN", "-Fp", "-Fc")
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return parseLsof(string(output)), nil
}

func parseLsof(output string) []Owner {
	var owners []Owner
	current := Owner{}
	flush := func() {
		if current.PID > 0 {
			owners = append(owners, current)
		}
		current = Owner{}
	}
	for _, line := range strings.Split(output, "\n") {
		if len(line) < 2 {
			continue
		}
		switch line[0] {
		case 'p':
			flush()
			current.PID, _ = strconv.Atoi(line[1:])
		case 'c':
			current.Command = line[1:]
		}
	}
	flush()
	return owners
}

func inspectWithSS(ctx context.Context, port int) ([]Owner, error) {
	cmd := exec.CommandContext(ctx, "ss", "-H", "-ltnp")
	output, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	return parseSS(string(output), port), nil
}

func parseSS(output string, port int) []Owner {
	needle := ":" + strconv.Itoa(port)
	var owners []Owner
	for _, line := range strings.Split(output, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		local := fields[3]
		if !strings.HasSuffix(local, needle) {
			continue
		}
		for _, match := range ssProcessPattern.FindAllStringSubmatch(line, -1) {
			pid, _ := strconv.Atoi(match[2])
			owners = append(owners, Owner{PID: pid, Command: match[1]})
		}
	}
	return owners
}

func terminatePID(pid int, force bool) error {
	signal := syscall.SIGTERM
	if force {
		signal = syscall.SIGKILL
	}
	if err := syscall.Kill(pid, signal); err != nil && !errors.Is(err, syscall.ESRCH) {
		return err
	}
	return nil
}
