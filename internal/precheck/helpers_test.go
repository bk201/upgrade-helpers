package precheck

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

func TestParseClusterVersion(t *testing.T) {
	tests := []struct {
		value               string
		major, minor, patch int
		wantError           bool
	}{
		{value: "v1.4.2", major: 1, minor: 4, patch: 2},
		{value: "1.8.0", major: 1, minor: 8, patch: 0},
		{value: "v1.8.0-rc1", major: 1, minor: 8, patch: 0},
		{value: "unknown", wantError: true},
	}
	for _, test := range tests {
		t.Run(test.value, func(t *testing.T) {
			got, err := ParseClusterVersion(test.value)
			if test.wantError {
				if err == nil {
					t.Fatal("expected an error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.Major != test.major || got.Minor != test.minor || got.Patch != test.patch {
				t.Fatalf("got %d.%d.%d", got.Major, got.Minor, got.Patch)
			}
		})
	}
}

func TestUsableIPv4Count(t *testing.T) {
	tests := []struct {
		name     string
		include  string
		excludes []string
		want     uint64
		wantErr  bool
	}{
		{name: "ordinary subnet", include: "192.168.1.0/24", want: 254},
		{name: "host bits normalized", include: "192.168.1.10/24", want: 254},
		{name: "network and broadcast excluded", include: "10.0.0.0/30", want: 2},
		{name: "small subnet", include: "10.0.0.0/31", want: 0},
		{name: "overlapping excludes merged", include: "10.0.0.0/24", excludes: []string{"10.0.0.0/28", "10.0.0.8/29"}, want: 239},
		{name: "exclude clipped", include: "10.0.0.0/24", excludes: []string{"9.0.0.0/8"}, want: 254},
		{name: "IPv6 rejected", include: "fd00::/64", wantErr: true},
		{name: "malformed exclusion", include: "10.0.0.0/24", excludes: []string{"bad"}, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := usableIPv4Count(test.include, test.excludes)
			if test.wantErr {
				if err == nil {
					t.Fatal("expected an error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != test.want {
				t.Fatalf("got %d, want %d", got, test.want)
			}
		})
	}
}

func TestWriteReport(t *testing.T) {
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	report := Report{
		SchemaVersion: "v1", ClusterVersion: "v1.8.0", StartedAt: now, FinishedAt: now,
		Checks: []Result{{ID: "nodes", Name: "Nodes", Scope: "node", Status: Warning, Summary: "warning", Nodes: []NodeResult{{Node: "node-1", Status: Warning, Summary: "soon"}}}},
	}
	report.Summary = Summarize(report.Checks)
	for _, format := range []string{"text", "json"} {
		t.Run(format, func(t *testing.T) {
			var output bytes.Buffer
			if err := WriteReport(&output, report, format); err != nil {
				t.Fatal(err)
			}
			if format == "text" {
				if !strings.Contains(output.String(), "[WARNING] Nodes") || !strings.Contains(output.String(), "node-1") {
					t.Fatalf("unexpected text report: %s", output.String())
				}
				return
			}
			var decoded Report
			if err := json.Unmarshal(output.Bytes(), &decoded); err != nil {
				t.Fatal(err)
			}
			if decoded.SchemaVersion != "v1" || decoded.Summary.Warning != 1 {
				t.Fatalf("unexpected JSON report: %+v", decoded)
			}
		})
	}
}

func TestRegistryIDsAreStableAndUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, check := range Registry() {
		if check.ID == "" || check.Name == "" || check.Run == nil {
			t.Fatalf("incomplete check registration: %+v", check)
		}
		if seen[check.ID] {
			t.Fatalf("duplicate check ID %q", check.ID)
		}
		seen[check.ID] = true
	}
	if len(seen) != 21 {
		t.Fatalf("got %d registered checks, want 21", len(seen))
	}
}
