//go:build unix

package main

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

type lockedBuffer struct {
	mu   sync.Mutex
	data []byte
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.data = append(b.data, p...)
	return len(p), nil
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.data)
}

func TestGracefulShutdownRunsLifecycleHooks(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "heron")
	build := exec.Command("go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build heron: %v\n%s", err, output)
	}

	port := unusedPort(t)
	outputPath := filepath.Join(dir, "hooks.log")
	configPath := filepath.Join(dir, "heron.yaml")
	startCommand := "echo start >> " + strconv.Quote(outputPath)
	tearDownCommand := "echo tearDown >> " + strconv.Quote(outputPath)
	contents := fmt.Sprintf("port: %d\nstartUp:\n  - %s\ntearDown:\n  - %s\napps: {}\n", port, strconv.Quote(startCommand), strconv.Quote(tearDownCommand))
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

	waitForListener(t, port, done, &processOutput)
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("heron exited with error: %v\n%s", err, processOutput.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("heron did not stop after interrupt\n%s", processOutput.String())
	}

	got, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "start\ntearDown\n" {
		t.Fatalf("hook output = %q, want startup then teardown", got)
	}
}

func TestInterruptDuringStartUpIsGraceful(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "heron")
	build := exec.Command("go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build heron: %v\n%s", err, output)
	}

	port := unusedPort(t)
	outputPath := filepath.Join(dir, "hooks.log")
	configPath := filepath.Join(dir, "heron.yaml")
	startCommand := "echo start >> " + strconv.Quote(outputPath) + "; sleep 30"
	tearDownCommand := "echo tearDown >> " + strconv.Quote(outputPath)
	contents := fmt.Sprintf("port: %d\nstartUp:\n  - %s\ntearDown:\n  - %s\napps: {}\n", port, strconv.Quote(startCommand), strconv.Quote(tearDownCommand))
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

	waitForFileContent(t, outputPath, "start\n", done, &processOutput)
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("heron exited with error after interrupt during startUp: %v\n%s", err, processOutput.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("heron did not stop after interrupt during startUp\n%s", processOutput.String())
	}

	got, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "start\ntearDown\n" {
		t.Fatalf("hook output = %q, want interrupted startup then teardown", got)
	}
}

func TestScheduledTaskIsCanceledBeforeTearDown(t *testing.T) {
	dir := t.TempDir()
	binary := filepath.Join(dir, "heron")
	build := exec.Command("go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build heron: %v\n%s", err, output)
	}

	port := unusedPort(t)
	outputPath := filepath.Join(dir, "lifecycle.log")
	configPath := filepath.Join(dir, "heron.yaml")
	startCommand := "echo start >> " + strconv.Quote(outputPath)
	taskCommand := "echo scheduled >> " + strconv.Quote(outputPath) + "; sleep 30"
	tearDownCommand := "echo tearDown >> " + strconv.Quote(outputPath)
	contents := fmt.Sprintf(
		"port: %d\nstartUp:\n  - %s\ntearDown:\n  - %s\nscheduledTasks:\n  blocking:\n    command: %s\n    every: 1h\n    timeout: 1h\n    runOnStart: true\napps: {}\n",
		port, strconv.Quote(startCommand), strconv.Quote(tearDownCommand), strconv.Quote(taskCommand),
	)
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

	waitForListener(t, port, done, &processOutput)
	waitForFileContent(t, outputPath, "start\nscheduled\n", done, &processOutput)
	if err := cmd.Process.Signal(os.Interrupt); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("heron exited with error: %v\n%s", err, processOutput.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("heron did not stop after interrupt\n%s", processOutput.String())
	}

	got, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "start\nscheduled\ntearDown\n" {
		t.Fatalf("lifecycle output = %q", got)
	}
}

func unusedPort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return port
}

func waitForListener(t *testing.T, port int, done <-chan error, output *lockedBuffer) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	address := fmt.Sprintf("127.0.0.1:%d", port)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			t.Fatalf("heron exited before listening: %v\n%s", err, output.String())
		default:
		}
		connection, err := net.DialTimeout("tcp", address, 50*time.Millisecond)
		if err == nil {
			_ = connection.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("heron did not start listening\n%s", output.String())
}

func waitForFileContent(t *testing.T, path, want string, done <-chan error, output *lockedBuffer) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case err := <-done:
			t.Fatalf("heron exited before startup hook became observable: %v\n%s", err, output.String())
		default:
		}
		got, err := os.ReadFile(path)
		if err == nil && string(got) == want {
			return
		}
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("startup hook did not become observable\n%s", output.String())
}
