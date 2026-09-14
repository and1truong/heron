package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadEnvFilesMultiline(t *testing.T) {
	for _, tc := range []struct{ name, input, want string }{
		{"reported curly quotes", "FOO=‘\n{ “foo”: “bar” }\n‘", "\n{ “foo”: “bar” }\n"},
		{"single quotes", "FOO='\n{ \"foo\": \"bar\" }\n'", "\n{ \"foo\": \"bar\" }\n"},
		{"double quotes", "FOO=\"first\nsecond\" # comment", "first\nsecond"},
		{"paired curly quotes", "FOO=‘first\nsecond’", "first\nsecond"},
		{"literal content", "export FOO='  first  \n\n# data\nKEY=value\n${HOME} {{UNSET}} $(echo nope)\n  last  '", "  first  \n\n# data\nKEY=value\n${HOME} {{UNSET}} $(echo nope)\n  last  "},
		{"escaped double quote", "FOO=\"first\\\"\nsecond\\nthird\"", "first\"\nsecond\nthird"},
		{"CRLF", "FOO='\r\n  data  \r\n'", "\n  data  \n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.WriteFile(filepath.Join(dir, "app.env"), []byte(tc.input+"\nAFTER=ok\n"), 0600); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(dir, "config.yaml")
			if err := os.WriteFile(path, []byte(fmt.Sprintf("apps:\n  api:\n    pwd: %q\n    launch: server\n    port: 1980\n    envFiles: [app.env]\n", dir)), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if got := cfg.Apps["api"].Env["FOO"]; got != tc.want {
				t.Fatalf("FOO = %q; want %q", got, tc.want)
			}
			if cfg.Apps["api"].Env["AFTER"] != "ok" {
				t.Fatal("assignment after multiline value lost")
			}
		})
	}
}

func TestParseEnvFileMultilineErrors(t *testing.T) {
	for _, input := range []string{
		"FOO='secret\nunterminated", "FOO=\"secret\nunterminated", "FOO=‘secret\nunterminated",
		"FOO='secret\nvalue' trailing", "FOO=‘secret\nvalue‘ trailing",
	} {
		_, err := parseEnvFile("# header\n" + input)
		if err == nil {
			t.Fatal("accepted invalid multiline value")
		}
		if !strings.Contains(err.Error(), "line 2:") || strings.Contains(err.Error(), "secret") {
			t.Fatalf("unexpected diagnostic: %v", err)
		}
	}
	_, err := parseEnvFile("FOO='first\nsecond'\nBAD\n")
	if err == nil || !strings.Contains(err.Error(), "line 3:") {
		t.Fatalf("wrong subsequent line: %v", err)
	}
}
