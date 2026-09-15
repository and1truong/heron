//go:build unix

package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Regression: a browser-held proxied connection (e.g. a WebSocket) pinned the
// drain phase for the whole shutdown budget, so StopAll inherited an already
// expired context and heron exited before the app was stopped.
func TestInterruptWithOpenProxiedConnectionStopsApp(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "heron")
	build := exec.Command("go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build heron: %v\n%s", err, output)
	}

	proxyPort := unusedPort(t)
	upstreamPort := unusedPort(t)
	scriptPath := filepath.Join(dir, "stream_server.py")
	stopMarker := filepath.Join(dir, "app-stopped")
	script := fmt.Sprintf(`import socket, time, signal
def stop(signum, frame):
    s.close()
    with open(%q, "w") as marker:
        marker.write("stopped")
    raise SystemExit(0)
signal.signal(signal.SIGTERM, stop)
s = socket.socket()
s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
s.bind(("127.0.0.1", %d))
s.listen(8)
while True:
    c, _ = s.accept()
    request = c.recv(65536)
    if request == b"QUIT":
        c.close()
        break
    try:
        c.sendall(b"HTTP/1.0 200 OK\r\nContent-Type: text/plain\r\n\r\n")
        while True:
            c.sendall(b"tick\n")
            time.sleep(0.2)
    except OSError:
        pass
`, stopMarker, upstreamPort)
	if err := os.WriteFile(scriptPath, []byte(script), 0600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// Ask this fixture to exit if a regression left it listening. Avoid
		// pgrep/pkill: /proc can describe a different PID namespace in containers.
		if conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", upstreamPort), time.Second); err == nil {
			_, _ = conn.Write([]byte("QUIT"))
			_ = conn.Close()
		}
	})

	configPath := filepath.Join(dir, "heron.yaml")
	contents := fmt.Sprintf("port: %d\nstopTimeout: 2s\napps:\n  slow:\n    pwd: %s\n    launch: python3 %s\n    port: %d\n", proxyPort, dir, scriptPath, upstreamPort)
	if err := os.WriteFile(configPath, []byte(contents), 0600); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(binary, "-c", configPath)
	var processOutput lockedBuffer
	cmd.Stdout = &processOutput
	cmd.Stderr = &processOutput
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	defer func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			<-done
		}
	}()

	waitForListener(t, proxyPort, done, &processOutput)

	// Hold an in-flight proxied request open, the way a browser WebSocket does.
	conn, err := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", proxyPort))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := fmt.Fprintf(conn, "GET / HTTP/1.1\r\nHost: slow.localhost\r\nConnection: keep-alive\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	if err := conn.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 4096)
	var seen []byte
	for !strings.Contains(string(seen), "tick") {
		n, err := conn.Read(buffer)
		if n > 0 {
			seen = append(seen, buffer[:n]...)
		}
		if err != nil {
			t.Fatalf("proxied stream ended before data: %v\n%s", err, processOutput.String())
		}
	}

	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("heron exited with error: %v\n%s", err, processOutput.String())
		}
	case <-time.After(15 * time.Second):
		t.Fatalf("heron did not stop within 15s of interrupt while a proxied connection was open\n%s", processOutput.String())
	}

	// The fixture itself must acknowledge SIGTERM, and its listening socket
	// must close. Global command-line matching can select stale host-namespace
	// PIDs and previously reported a live app even after successful shutdown.
	if _, err := os.Stat(stopMarker); err != nil {
		t.Fatalf("app did not acknowledge graceful shutdown: %v\n%s", err, processOutput.String())
	}
	if probe, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", upstreamPort), time.Second); err == nil {
		_ = probe.Close()
		t.Fatalf("app listener survived Heron shutdown\n%s", processOutput.String())
	}
}
