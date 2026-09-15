package background

import (
	"context"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestNativeStatus(t *testing.T) {
	for _, tc := range []struct {
		name, platform, output string
		commandError           bool
		want                   ProcessState
		wantError              bool
	}{
		{"linux-running", "linux", "LoadState=loaded\nActiveState=active\nMainPID=234\n", false, ProcessState{Loaded: true, PID: 234, Active: true}, false},
		{"linux-missing", "linux", "LoadState=not-found\nActiveState=inactive\nMainPID=0\n", true, ProcessState{}, false},
		{"linux-failed", "linux", "LoadState=loaded\nActiveState=failed\nMainPID=0\n", false, ProcessState{Loaded: true, Failed: true}, false},
		{"linux-stopping", "linux", "LoadState=loaded\nActiveState=deactivating\nMainPID=234\n", false, ProcessState{Loaded: true, PID: 234, Stopping: true}, false},
		{"linux-no-bus", "linux", "Failed to connect to bus: No medium found", true, ProcessState{}, true},
		{"mac-running", "darwin", "gui/501/heron = {\n\tstate = running\n\tpid = 234\n\tlast exit code = (never exited)\n}", false, ProcessState{Loaded: true, PID: 234, Active: true}, false},
		{"mac-missing", "darwin", "Could not find service heron in domain for user gui: 501", true, ProcessState{}, false},
		{"mac-no-session", "darwin", "Could not find domain for", true, ProcessState{}, true},
		{"mac-failed", "darwin", "state = not running\nlast exit code = 1", false, ProcessState{Loaded: true, Failed: true}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := Native{OS: tc.platform, Run: func(context.Context, string, ...string) (string, error) {
				if tc.commandError {
					return tc.output, errors.New("command failed")
				}
				return tc.output, nil
			}}
			got, err := n.Status(context.Background())
			if (err != nil) != tc.wantError || got != tc.want {
				t.Fatalf("got %+v, %v; want %+v error=%v", got, err, tc.want, tc.wantError)
			}
		})
	}
}

func TestDefinitionsEscapePathsAndNeverEnableLoginStartup(t *testing.T) {
	n := Native{Instance: Instance{ID: "heron-test", Dir: "/tmp/a b&<%$\""}}
	spec := Spec{Executable: "/tmp/a b&<%$\"/heron"}
	unit := n.Unit(spec)
	if strings.Contains(unit, "[Install]") || !strings.Contains(unit, "ExecStart=:") || !strings.Contains(unit, "%%$") || !strings.Contains(unit, `\"`) {
		t.Fatalf("invalid unit: %s", unit)
	}
	plist := n.Plist(spec)
	d := xml.NewDecoder(strings.NewReader(plist))
	var texts []string
	for {
		token, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if start, ok := token.(xml.StartElement); ok && start.Name.Local == "string" {
			var value string
			if err := d.DecodeElement(&value, &start); err != nil {
				t.Fatal(err)
			}
			texts = append(texts, value)
		}
	}
	if !strings.Contains(strings.Join(texts, "\n"), spec.Executable) {
		t.Fatalf("plist changed executable: %v", texts)
	}
	if !strings.Contains(plist, "<key>KeepAlive</key><false/>") {
		t.Fatal("service must not respawn during stop")
	}
}

func TestNativeStopAddressesManagerNotPID(t *testing.T) {
	for _, platform := range []string{"linux", "darwin"} {
		var invoked string
		n := Native{OS: platform, UID: 501, Instance: Instance{ID: "heron-test"}, Run: func(_ context.Context, command string, args ...string) (string, error) {
			invoked = command + " " + strings.Join(args, " ")
			return "", nil
		}}
		if err := n.Stop(context.Background()); err != nil {
			t.Fatal(err)
		}
		want := "systemctl --user stop --no-block heron-test.service"
		if platform == "darwin" {
			want = "launchctl kill SIGTERM gui/501/heron-test"
		}
		if invoked != want {
			t.Fatalf("got %s, want %s", invoked, want)
		}
	}
}

func TestStartRegistersWithoutEnablingAndCreatesPrivateLog(t *testing.T) {
	for _, platform := range []string{"linux", "darwin"} {
		t.Run(platform, func(t *testing.T) {
			var calls []string
			n := Native{OS: platform, UID: 501, Instance: Instance{ID: "heron-test", Dir: t.TempDir()}, Run: func(_ context.Context, command string, args ...string) (string, error) {
				calls = append(calls, command+" "+strings.Join(args, " "))
				return "", nil
			}}
			if err := n.Start(context.Background(), Spec{Executable: "/bin/true"}); err != nil {
				t.Fatal(err)
			}
			joined := strings.Join(calls, "\n")
			if strings.Contains(joined, "enable") || strings.Contains(joined, "sudo") {
				t.Fatalf("unexpected registration: %s", joined)
			}
			if platform == "darwin" && !strings.Contains(joined, "launchctl bootstrap gui/501 ") {
				t.Fatalf("missing bootstrap: %s", joined)
			}
			if platform == "linux" && (len(calls) != 3 || !strings.Contains(calls[0], "link ") || !strings.Contains(calls[1], "daemon-reload") || !strings.Contains(calls[2], "start heron-test.service")) {
				t.Fatalf("incorrect ordering: %s", joined)
			}
			info, err := os.Stat(n.Instance.LogPath())
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0600 {
				t.Fatalf("log permissions: %v", info.Mode())
			}
		})
	}
}

func TestSystemdDefinitionAcceptedByParser(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("systemd parser requires Linux")
	}
	parser, err := exec.LookPath("systemd-analyze")
	if err != nil {
		t.Skip("systemd-analyze not installed")
	}
	n := Native{Instance: Instance{ID: "heron-test", Dir: filepath.Join(t.TempDir(), "spaces % $ and quotes\"")}}
	path := filepath.Join(t.TempDir(), "heron-test.service")
	if err := WriteFile(path, []byte(n.Unit(Spec{Executable: "/bin/true"}))); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(parser, "verify", path).CombinedOutput(); err != nil {
		t.Fatalf("systemd rejected unit: %v\n%s", err, output)
	}
}
