package main

import (
	"bytes"
	"encoding/json"
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

func TestCheckSelectionValidationHappensBeforeKubeconfig(t *testing.T) {
	var output, stderr bytes.Buffer
	if got := run([]string{"--kubeconfig", filepath.Join(t.TempDir(), "does-not-exist"), "--checks", "not-a-check"}, strings.NewReader(""), &output, &stderr); got != 2 {
		t.Fatalf("got exit %d, want 2; stderr=%s", got, stderr.String())
	}
	if !strings.Contains(stderr.String(), "unknown check ID") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestCheckSelectionUsageErrors(t *testing.T) {
	tests := [][]string{
		{"--checks", "node-status,"},
		{"--checks", ""},
		{"--checks", "node-status", "--list-checks"},
	}
	for _, args := range tests {
		var output, stderr bytes.Buffer
		if got := run(args, strings.NewReader(""), &output, &stderr); got != 2 {
			t.Fatalf("args %v: got exit %d, want 2; stderr=%s", args, got, stderr.String())
		}
	}
}

func TestListChecksWorksOffline(t *testing.T) {
	var output, stderr bytes.Buffer
	if got := run([]string{"--list-checks"}, strings.NewReader(""), &output, &stderr); got != 0 {
		t.Fatalf("got exit %d, want 0; stderr=%s", got, stderr.String())
	}
	if !strings.Contains(output.String(), "ID                       SCOPE    DISPLAY NAME") || !strings.Contains(output.String(), "node-status              cluster  Node Status") {
		t.Fatalf("unexpected listing: %q", output.String())
	}

	output.Reset()
	if got := run([]string{"--list-checks", "--output=json"}, strings.NewReader(""), &output, &stderr); got != 0 {
		t.Fatalf("JSON listing exit %d; stderr=%s", got, stderr.String())
	}
	var listing struct {
		SchemaVersion string `json:"schemaVersion"`
		Checks        []struct {
			ID string `json:"id"`
		} `json:"checks"`
	}
	if err := json.Unmarshal(output.Bytes(), &listing); err != nil {
		t.Fatal(err)
	}
	if listing.SchemaVersion != "v1" || len(listing.Checks) == 0 || listing.Checks[0].ID != "certificates" {
		t.Fatalf("unexpected JSON listing: %+v", listing)
	}
}
