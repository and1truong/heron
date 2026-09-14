package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAppFlagValidation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "heron.yaml")
	if err := os.WriteFile(path, []byte("apps:\n  api:\n    pwd: .\n    launch: sleep 60\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, prefix := range [][]string{nil, {"tui"}} {
		for _, flags := range [][]string{{"--app=missing"}, {"--app", "missing"}, {"--app="}, {"--app", " "}} {
			args := append(append([]string{}, prefix...), "-c", path)
			args = append(args, flags...)
			err := runArgs(args, &bytes.Buffer{})
			if err == nil || (!strings.Contains(err.Error(), "unknown app") && !strings.Contains(err.Error(), "non-empty app name")) {
				t.Fatalf("%v: %v", args, err)
			}
		}
	}
	if err := runArgs([]string{"-c", path, "--app=api", "doctor"}, &bytes.Buffer{}); err == nil {
		t.Fatal("doctor must reject --app")
	}
}
