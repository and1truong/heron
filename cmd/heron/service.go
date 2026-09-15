package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/and1truong/heron/internal/background"
	"github.com/and1truong/heron/internal/config"
	"github.com/and1truong/heron/internal/webui"
)

type exitError struct{ code int }

func (e exitError) Error() string { return "" }

func isServiceCommand(command string) bool {
	return command == "start" || command == "stop" || command == "restart" || command == "status"
}

func runServiceArgs(command string, args []string, output io.Writer) error {
	flags := flag.NewFlagSet("heron "+command, flag.ContinueOnError)
	flags.SetOutput(output)
	path := flags.String("c", "", "configuration file (default ~/.config/heron.yaml)")
	timeout := flags.Duration("timeout", 2*time.Minute, "maximum time to acquire the lifecycle lock and complete the command")
	flags.Usage = func() {
		fmt.Fprintf(output, "Manage the background Heron service (web UI enabled automatically).\n\nUsage: heron %s [-c FILE] [--timeout 2m]\n\nOptions:\n", command)
		flags.PrintDefaults()
		fmt.Fprintf(output, "\nExample:\n  heron %s -c ./config.yaml\n\nStatus exit codes: 0 running, 3 stopped, 1 transitional/failed/error.\n", command)
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("%s: unexpected arguments: %v", command, flags.Args())
	}
	if *timeout <= 0 {
		return fmt.Errorf("--timeout must be positive")
	}
	resolved, err := resolveConfigPath(flags, *path)
	if err != nil {
		return err
	}
	instance, err := background.Locate(resolved)
	if err != nil {
		return err
	}
	backend, err := background.NewBackend(instance)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	controller := background.Controller{Instance: instance, Backend: backend}
	if command == "status" {
		status, err := controller.Status(ctx)
		if err != nil {
			return err
		}
		printServiceStatus(output, instance, status)
		if status.State == "running" {
			return nil
		}
		if status.State == "stopped" {
			return exitError{3}
		}
		return exitError{1}
	}
	unlock, err := instance.Lock(ctx)
	if err != nil {
		return fmt.Errorf("service lifecycle lock: %w", err)
	}
	defer unlock()
	if command == "stop" {
		if err := controller.Stop(ctx); err != nil {
			return err
		}
		fmt.Fprintln(output, "Heron service stopped.")
		return nil
	}
	status, err := controller.Status(ctx)
	if err != nil {
		return err
	}
	if command == "start" && (status.State == "running" || status.State == "starting") {
		status, err = controller.WaitReady(ctx)
		if err != nil {
			return err
		}
		fmt.Fprintln(output, "Heron service already running.")
		printServiceStatus(output, instance, status)
		return nil
	}
	if status.State == "stopping" {
		return fmt.Errorf("service is still stopping; check heron status before retrying")
	}
	spec, err := background.NewSpec(instance)
	if err != nil {
		return err
	}
	if command == "restart" {
		// Restart uses the launch environment and cwd of the existing service.
		// A fresh stop/start explicitly captures the current shell instead.
		var saved background.Spec
		if err := background.ReadJSON(instance.SpecPath(), &saved); err == nil {
			spec.Environment, spec.Directory = saved.Environment, saved.Directory
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if err := spec.NewToken(); err != nil {
		return err
	}
	// Validate in the service's environment before stopping a healthy instance.
	pending := filepath.Join(instance.Dir, "pending.json")
	if err := background.WriteJSON(pending, spec); err != nil {
		return err
	}
	defer os.Remove(pending)
	validation := exec.CommandContext(ctx, spec.Executable, "__service-validate", pending)
	if data, err := validation.CombinedOutput(); err != nil {
		return fmt.Errorf("service configuration validation failed: %w: %s", err, strings.TrimSpace(string(data)))
	}
	if err := controller.Stop(ctx); err != nil {
		return err
	}
	if err := background.WriteJSON(instance.SpecPath(), spec); err != nil {
		return err
	}
	if err := backend.Start(ctx, spec); err != nil {
		cleanup, cancelCleanup := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelCleanup()
		return errors.Join(fmt.Errorf("start service: %w (log: %s)", err, instance.LogPath()), controller.Stop(cleanup))
	}
	status, err = controller.WaitReady(ctx)
	if err != nil {
		// Startup can time out while a hook is running. Ask the manager to stop
		// that attempt; a later start must not create an overlapping process.
		cleanup, cancelCleanup := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelCleanup()
		stopErr := controller.Stop(cleanup)
		return errors.Join(err, stopErr)
	}
	printServiceStatus(output, instance, status)
	return nil
}

func printServiceStatus(output io.Writer, instance background.Instance, status background.Status) {
	fmt.Fprintf(output, "Heron service: %s\nConfig: %s\nLog: %s\n", status.State, instance.Config, instance.LogPath())
	if status.PID != 0 {
		fmt.Fprintf(output, "PID: %d\n", status.PID)
	}
	if status.PID != 0 && !status.StartedAt.IsZero() {
		fmt.Fprintf(output, "Uptime: %s\n", time.Since(status.StartedAt).Truncate(time.Second))
	}
	if status.URL != "" {
		fmt.Fprintf(output, "Heron UI: %s\n", status.URL)
	}
	if status.Error != "" {
		fmt.Fprintf(output, "Error: %s\n", status.Error)
	}
}

func readServiceSpec(path string) (background.Spec, error) {
	var spec background.Spec
	if !filepath.IsAbs(path) {
		return spec, fmt.Errorf("service spec path must be absolute")
	}
	if err := background.ReadJSON(path, &spec); err != nil {
		return spec, err
	}
	if !filepath.IsAbs(spec.Config) || !filepath.IsAbs(spec.Directory) || spec.Token == "" {
		return spec, fmt.Errorf("invalid service launch spec")
	}
	if err := os.Chdir(spec.Directory); err != nil {
		return spec, err
	}
	os.Clearenv()
	for _, item := range spec.Environment {
		key, value, ok := strings.Cut(item, "=")
		if !ok {
			return spec, fmt.Errorf("invalid service environment entry")
		}
		if err := os.Setenv(key, value); err != nil {
			return spec, err
		}
	}
	return spec, nil
}

func runServiceWorker(command string, args []string) (result error) {
	if len(args) != 1 {
		return fmt.Errorf("internal service command requires a launch spec")
	}
	spec, err := readServiceSpec(args[0])
	if err != nil {
		return err
	}
	if command == "__service-validate" {
		cfg, err := config.LoadStrict(spec.Config)
		if err != nil {
			return err
		}
		// Construction validates the reserved UI hostname without starting apps.
		_, err = webui.New(cfg, spec.Config, nil, nil)
		return err
	}
	instance := background.Instance{Dir: filepath.Dir(args[0]), Config: spec.Config}
	log, err := os.OpenFile(instance.LogPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer log.Close()
	oldOut, oldErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = log, log
	defer func() { os.Stdout, os.Stderr = oldOut, oldErr }()
	state := background.RuntimeState{Token: spec.Token, PID: os.Getpid(), State: "starting", StartedAt: time.Now()}
	writeState := func() error { return background.WriteJSON(instance.StatePath(), state) }
	defer func() {
		state.State = "stopped"
		if result != nil {
			state.State, state.Error = "failed", result.Error()
			fmt.Fprintln(log, result)
		}
		result = errors.Join(result, writeState())
	}()
	lifecycle := &runtimeLifecycle{
		starting: func() error { state.State = "starting"; return writeState() },
		ready:    func(url string) error { state.State, state.URL = "running", url; return writeState() },
		stopping: func() {
			state.State = "stopping"
			if err := writeState(); err != nil {
				fmt.Fprintln(log, err)
			}
		},
	}
	return runModeWithLifecycle(spec.Config, false, true, "", log, lifecycle)
}
