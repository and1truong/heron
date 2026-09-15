package main

import (
	"bytes"
	"testing"
)

func TestServiceCommandsRejectInvalidArgumentsBeforeSideEffects(t *testing.T) {
	for _, args := range [][]string{
		{"start", "--app", "api"}, {"stop", "extra"},
		{"restart", "--timeout", "0s"}, {"status", "--timeout", "-1s"},
		{"start", "-c", ""},
	} {
		if err := runArgs(args, &bytes.Buffer{}); err == nil {
			t.Fatalf("accepted invalid arguments: %v", args)
		}
	}
}
