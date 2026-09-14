package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sort"
	"strings"

	"github.com/and1truong/heron/internal/config"
	"github.com/and1truong/heron/internal/portguard"
)

func preflightRequiredPorts(ctx context.Context, cfg config.RuntimeConfig, logger *slog.Logger) error {
	conflicts, err := portguard.Check(ctx, portRequirements(cfg))
	if err != nil {
		return fmt.Errorf("port preflight: %w", err)
	}
	if len(conflicts) == 0 {
		return nil
	}

	printPortConflicts(os.Stderr, conflicts)
	if !interactiveConsole(os.Stdin, os.Stderr) {
		return fmt.Errorf("port preflight: %d required port(s) are occupied; rerun Heron in an interactive terminal to release them, or stop the owning processes manually", len(conflicts))
	}
	for _, conflict := range conflicts {
		if len(conflict.Owners) == 0 {
			return fmt.Errorf("port preflight: port %d is occupied but its owning process could not be identified; stop it manually and retry", conflict.Port)
		}
	}

	fmt.Fprint(os.Stderr, "Release these ports and continue? [y/N] ")
	answer, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && err != io.EOF {
		return fmt.Errorf("read port release confirmation: %w", err)
	}
	answer = strings.ToLower(strings.TrimSpace(answer))
	if answer != "y" && answer != "yes" {
		return fmt.Errorf("startup canceled: required ports remain occupied")
	}

	for _, conflict := range conflicts {
		for _, owner := range conflict.Owners {
			logger.Warn("releasing occupied port", "port", conflict.Port, "pid", owner.PID, "process", owner.Command)
		}
	}
	if err := portguard.Release(ctx, conflicts, cfg.StopTimeout); err != nil {
		return fmt.Errorf("release occupied ports: %w", err)
	}
	logger.Info("released occupied ports", "count", len(conflicts))
	return nil
}

func portRequirements(cfg config.RuntimeConfig) []portguard.Requirement {
	requirements := []portguard.Requirement{{Port: cfg.Port, Purpose: "Heron HTTP/gRPC listener"}}
	ids := make([]string, 0, len(cfg.Apps))
	for id := range cfg.Apps {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		app := cfg.Apps[id]
		for _, endpoint := range app.EndpointList() {
			name := endpoint.Name
			if name == "" {
				name = "default"
			}
			requirements = append(requirements, portguard.Requirement{
				Port:    endpoint.Port,
				Purpose: fmt.Sprintf("app %s endpoint %s backend", id, name),
			})
			if endpoint.Protocol == config.ProtocolTCP {
				requirements = append(requirements, portguard.Requirement{
					Port:    endpoint.ListenPort,
					Purpose: fmt.Sprintf("app %s endpoint %s TCP listener", id, name),
				})
			}
		}
	}
	return requirements
}

func printPortConflicts(output io.Writer, conflicts []portguard.Conflict) {
	fmt.Fprintf(output, "%d required port(s) are already in use:\n", len(conflicts))
	for _, conflict := range conflicts {
		fmt.Fprintf(output, "  port %d (%s)\n", conflict.Port, strings.Join(conflict.Purposes, ", "))
		if len(conflict.Owners) == 0 {
			fmt.Fprintln(output, "    owner: unknown")
			continue
		}
		for _, owner := range conflict.Owners {
			command := owner.Command
			if command == "" {
				command = "unknown command"
			}
			fmt.Fprintf(output, "    PID %d  %s\n", owner.PID, command)
		}
	}
}

func interactiveConsole(input, output *os.File) bool {
	inputInfo, inputErr := input.Stat()
	outputInfo, outputErr := output.Stat()
	return inputErr == nil && outputErr == nil && inputInfo.Mode()&os.ModeCharDevice != 0 && outputInfo.Mode()&os.ModeCharDevice != 0
}
