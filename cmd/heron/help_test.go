package main

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestHelpDoesNotLoadConfiguration(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "missing.yaml")
	for _, args := range [][]string{
		{"help"}, {"-h"}, {"--help"}, {"-c", missing, "--help"},
		{"help", "start"}, {"start", "-h"}, {"start", "-c", missing, "--help"},
		{"help", "stop"}, {"stop", "--help"},
		{"help", "restart"}, {"restart", "--help"},
		{"help", "status"}, {"status", "--help"},
		{"help", "doctor"}, {"doctor", "-h"}, {"doctor", "--help"},
		{"doctor", "-c", missing, "--help"},
		{"help", "ui"}, {"ui", "-h"}, {"ui", "-c", missing, "--help"},
		{"help", "tui"}, {"tui", "-h"}, {"tui", "-c", missing, "--help"},
	} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var output bytes.Buffer
			if err := runArgs(args, &output); err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{"Usage:", "-c", "configuration file", "Example"} {
				if !strings.Contains(output.String(), want) {
					t.Fatalf("help missing %q: %s", want, output.String())
				}
			}
		})
	}
}

func TestHelpAliasesMatch(t *testing.T) {
	for _, pair := range [][2][]string{
		{{"help"}, {"--help"}},
		{{"help", "doctor"}, {"doctor", "--help"}},
		{{"help", "tui"}, {"tui", "--help"}},
		{{"help", "ui"}, {"ui", "--help"}},
		{{"help", "start"}, {"start", "--help"}},
		{{"help", "stop"}, {"stop", "--help"}},
		{{"help", "restart"}, {"restart", "--help"}},
		{{"help", "status"}, {"status", "--help"}},
	} {
		var first, second bytes.Buffer
		if err := runArgs(pair[0], &first); err != nil {
			t.Fatal(err)
		}
		if err := runArgs(pair[1], &second); err != nil {
			t.Fatal(err)
		}
		if first.String() != second.String() {
			t.Fatalf("help aliases differ: %q vs %q", first.String(), second.String())
		}
	}
}

func TestHelpRejectsInvalidTopics(t *testing.T) {
	for _, args := range [][]string{
		{"help", "unknown"},
		{"help", "doctor", "extra"},
		{"help", "doctor", "-c", "config.yaml"},
	} {
		var output bytes.Buffer
		if err := runArgs(args, &output); err == nil || !strings.Contains(err.Error(), "help:") {
			t.Fatalf("runArgs(%q) error = %v", args, err)
		}
	}
}

func TestHelpWithoutHomeDirectory(t *testing.T) {
	homeVariable := "HOME"
	switch runtime.GOOS {
	case "windows":
		homeVariable = "USERPROFILE"
	case "plan9":
		homeVariable = "home"
	}
	t.Setenv(homeVariable, "")
	if err := os.Unsetenv(homeVariable); err != nil {
		t.Fatal(err)
	}
	TestHelpDoesNotLoadConfiguration(t)
	TestHelpAliasesMatch(t)
	TestHelpRejectsInvalidTopics(t)

	for _, args := range [][]string{nil, {"doctor"}} {
		if err := runArgs(args, &bytes.Buffer{}); err == nil {
			t.Fatalf("runArgs(%q) should fail when the default path cannot be resolved", args)
		}
	}
}
