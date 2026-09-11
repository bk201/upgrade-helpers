package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestUsageAndValidationExitCodes(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want int
	}{
		{name: "help", args: []string{"--help"}, want: 0},
		{name: "bad output", args: []string{"--output=yaml"}, want: 2},
		{name: "bad timeout", args: []string{"--timeout=0"}, want: 2},
		{name: "extra argument", args: []string{"extra"}, want: 2},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output, stderr bytes.Buffer
			if got := run(test.args, strings.NewReader(""), &output, &stderr); got != test.want {
				t.Fatalf("got exit %d, want %d; stderr=%s", got, test.want, stderr.String())
			}
		})
	}
}

func TestPrepareLogRequiresYesWhenNonInteractive(t *testing.T) {
	path := filepath.Join(t.TempDir(), "report.log")
	if err := os.WriteFile(path, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := prepareLog(path, false, strings.NewReader("y\n"), &bytes.Buffer{}); err == nil {
		t.Fatal("expected non-interactive overwrite to fail")
	}
	writer, closeLog, err := prepareLog(path, true, strings.NewReader(""), &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Write([]byte("new")); err != nil {
		t.Fatal(err)
	}
	if err := closeLog(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "new" {
		t.Fatalf("got %q", data)
	}
}
