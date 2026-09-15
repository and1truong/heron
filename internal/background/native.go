package background

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

type ProcessState struct {
	Loaded   bool
	PID      int
	Active   bool
	Failed   bool
	Stopping bool
}

type Backend interface {
	Status(context.Context) (ProcessState, error)
	Start(context.Context, Spec) error
	Stop(context.Context) error
	Unload(context.Context) error
}

type Native struct {
	Instance Instance
	OS       string
	UID      int
	Run      func(context.Context, string, ...string) (string, error)
}

func NewBackend(i Instance) (*Native, error) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		return nil, fmt.Errorf("background service mode requires macOS launchd or Linux systemd --user")
	}
	return &Native{Instance: i, OS: runtime.GOOS, UID: os.Getuid(), Run: runCommand}, nil
}

func runCommand(ctx context.Context, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	// Stable diagnostics/status parsing, regardless of the invoking shell's locale.
	cmd.Env = append(os.Environ(), "LC_ALL=C")
	data, err := cmd.CombinedOutput()
	if err != nil {
		return string(data), fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(data)))
	}
	return string(data), nil
}

func (n *Native) target() string { return fmt.Sprintf("gui/%d/%s", n.UID, n.Instance.ID) }
func (n *Native) unit() string   { return n.Instance.ID + ".service" }

func (n *Native) Status(ctx context.Context) (ProcessState, error) {
	if n.OS == "darwin" {
		out, err := n.Run(ctx, "launchctl", "print", n.target())
		if err != nil {
			if strings.Contains(out, "Could not find service") {
				return ProcessState{}, nil
			}
			return ProcessState{}, fmt.Errorf("cannot query user launchd (requires a macOS login session): %w", err)
		}
		s := ProcessState{Loaded: true}
		for _, line := range strings.Split(out, "\n") {
			key, value, ok := strings.Cut(strings.TrimSpace(line), " = ")
			if !ok {
				continue
			}
			switch key {
			case "pid":
				s.PID, _ = strconv.Atoi(value)
			case "state":
				s.Active = value == "running"
				s.Stopping = value == "terminating"
			case "last exit code":
				s.Failed = value != "0" && value != "(never exited)"
			case "last terminating signal":
				s.Failed = value != "0"
			}
		}
		return s, nil
	}
	out, err := n.Run(ctx, "systemctl", "--user", "show", n.unit(), "--property=LoadState,ActiveState,SubState,MainPID,ExecMainStatus,Result")
	values := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		key, value, ok := strings.Cut(line, "=")
		if ok {
			values[key] = value
		}
	}
	if values["LoadState"] == "not-found" {
		return ProcessState{}, nil
	}
	if err != nil {
		return ProcessState{}, fmt.Errorf("cannot query systemd user manager (requires a user session and bus): %w", err)
	}
	if values["LoadState"] != "loaded" {
		return ProcessState{}, fmt.Errorf("unexpected systemd load state: %q", values["LoadState"])
	}
	pid, _ := strconv.Atoi(values["MainPID"])
	state := values["ActiveState"]
	return ProcessState{Loaded: true, PID: pid, Active: state == "active" || state == "activating", Stopping: state == "deactivating", Failed: state == "failed"}, nil
}

func (n *Native) Start(ctx context.Context, spec Spec) error {
	// launchd opens its stdout file before executing the worker. Create it with
	// private permissions first, rather than inheriting launchd's default umask.
	log, err := os.OpenFile(n.Instance.LogPath(), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	err = log.Chmod(0600)
	closeErr := log.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if n.OS == "darwin" {
		path := filepath.Join(n.Instance.Dir, "service.plist")
		if err := WriteFile(path, []byte(n.Plist(spec))); err != nil {
			return err
		}
		_, err := n.Run(ctx, "launchctl", "bootstrap", fmt.Sprintf("gui/%d", n.UID), path)
		return err
	}
	path := filepath.Join(n.Instance.Dir, n.unit())
	if err := WriteFile(path, []byte(n.Unit(spec))); err != nil {
		return err
	}
	if _, err := n.Run(ctx, "systemctl", "--user", "link", path); err != nil {
		return err
	}
	if _, err := n.Run(ctx, "systemctl", "--user", "daemon-reload"); err != nil {
		return err
	}
	_, err = n.Run(ctx, "systemctl", "--user", "start", n.unit())
	return err
}

func (n *Native) Stop(ctx context.Context) error {
	if n.OS == "darwin" {
		_, err := n.Run(ctx, "launchctl", "kill", "SIGTERM", n.target())
		return err
	}
	// Request the stop job without blocking systemctl. The controller polls actual
	// process exit; hitting the CLI timeout must never escalate to an unsafe kill.
	_, err := n.Run(ctx, "systemctl", "--user", "stop", "--no-block", n.unit())
	return err
}

func (n *Native) Unload(ctx context.Context) error {
	if n.OS == "darwin" {
		_, err := n.Run(ctx, "launchctl", "bootout", n.target())
		return err
	}
	// Keep the linked (but never enabled) unit for later starts and diagnostics.
	return nil
}

func systemdQuote(value string) string {
	value = strings.ReplaceAll(value, "%", "%%")
	return strconv.Quote(value)
}

func (n *Native) Unit(spec Spec) string {
	return fmt.Sprintf("[Unit]\nDescription=Heron local development supervisor\n\n[Service]\nType=exec\nExecStart=:%s __service-run %s\nRestart=no\nKillMode=process\nTimeoutStopSec=infinity\nSendSIGKILL=no\nStandardInput=null\nStandardOutput=journal\nStandardError=journal\n", systemdQuote(spec.Executable), systemdQuote(n.Instance.SpecPath()))
}

func xmlText(s string) string {
	var out bytes.Buffer
	_ = xml.EscapeText(&out, []byte(s))
	return out.String()
}

func (n *Native) Plist(spec Spec) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict>
<key>Label</key><string>%s</string>
<key>ProgramArguments</key><array><string>%s</string><string>__service-run</string><string>%s</string></array>
<key>RunAtLoad</key><true/>
<key>KeepAlive</key><false/>
<key>AbandonProcessGroup</key><true/>
<key>StandardOutPath</key><string>%s</string>
<key>StandardErrorPath</key><string>%s</string>
</dict></plist>
`, xmlText(n.Instance.ID), xmlText(spec.Executable), xmlText(n.Instance.SpecPath()), xmlText(n.Instance.LogPath()), xmlText(n.Instance.LogPath()))
}
