package precheck

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"
)

func TestSelectChecks(t *testing.T) {
	all := Registry()
	tests := []struct {
		name    string
		ids     []string
		wantIDs []string
		wantErr string
	}{
		{name: "nil means all", ids: nil, wantIDs: registryIDs(all)},
		{name: "one", ids: []string{"pod-status"}, wantIDs: []string{"pod-status"}},
		{
			name:    "trims deduplicates and preserves registry order",
			ids:     []string{" pod-status ", "node-status", "pod-status"},
			wantIDs: []string{"node-status", "pod-status"},
		},
		{name: "empty element", ids: []string{"node-status", ""}, wantErr: "empty check ID"},
		{name: "whitespace element", ids: []string{"  "}, wantErr: "empty check ID"},
		{name: "unknown", ids: []string{"does-not-exist"}, wantErr: "unknown check ID"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := SelectChecks(test.ids)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("error = %v, want substring %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if gotIDs := checkIDs(got); !equalStrings(gotIDs, test.wantIDs) {
				t.Fatalf("IDs = %v, want %v", gotIDs, test.wantIDs)
			}
		})
	}
}

func TestRunUsesOnlySelectedChecksAndPreservesOrder(t *testing.T) {
	called := make([]string, 0, 2)
	var calledMu sync.Mutex
	checks := []Check{
		{ID: "first", Name: "First", Scope: "cluster", Run: func(context.Context, *Environment) Result {
			calledMu.Lock()
			defer calledMu.Unlock()
			called = append(called, "first")
			return NewResult(Pass, "ok")
		}},
		{ID: "second", Name: "Second", Scope: "cluster", Run: func(context.Context, *Environment) Result {
			calledMu.Lock()
			defer calledMu.Unlock()
			called = append(called, "second")
			return NewResult(Warning, "check this")
		}},
	}
	env := &Environment{}
	report := Run(context.Background(), env, checks)
	if got := checkResultIDs(report.Checks); !equalStrings(got, []string{"first", "second"}) {
		t.Fatalf("report IDs = %v", got)
	}
	if report.Summary.Pass != 1 || report.Summary.Warning != 1 || report.Summary.Fail != 0 || report.Summary.Error != 0 {
		t.Fatalf("unexpected summary: %+v", report.Summary)
	}
	if len(called) != 2 {
		t.Fatalf("called %v, want both selected checks", called)
	}
}

func TestWriteCheckList(t *testing.T) {
	metadata := []CheckMetadata{{ID: "a", Scope: "cluster", Name: "A"}, {ID: "b", Scope: "node", Name: "B"}}

	var text bytes.Buffer
	if err := WriteCheckList(&text, metadata, "text"); err != nil {
		t.Fatal(err)
	}
	if want := "ID  SCOPE    DISPLAY NAME\na   cluster  A\nb   node     B\n"; text.String() != want {
		t.Fatalf("text listing = %q, want %q", text.String(), want)
	}

	var encoded bytes.Buffer
	if err := WriteCheckList(&encoded, metadata, "json"); err != nil {
		t.Fatal(err)
	}
	var got CheckList
	if err := json.Unmarshal(encoded.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.SchemaVersion != "v1" || len(got.Checks) != 2 || got.Checks[1].ID != "b" {
		t.Fatalf("JSON listing = %+v", got)
	}
}

func TestListChecksMatchesRegistry(t *testing.T) {
	registered := Registry()
	metadata := ListChecks()
	if len(metadata) != len(registered) {
		t.Fatalf("metadata has %d checks, registry has %d", len(metadata), len(registered))
	}
	for index, check := range registered {
		got := metadata[index]
		if got.ID != check.ID || got.Scope != check.Scope || got.Name != check.Name {
			t.Fatalf("metadata[%d] = %+v, registry[%d] = %+v", index, got, index, check)
		}
	}
}

func registryIDs(checks []Check) []string {
	return checkIDs(checks)
}

func checkIDs(checks []Check) []string {
	ids := make([]string, 0, len(checks))
	for _, check := range checks {
		ids = append(ids, check.ID)
	}
	return ids
}

func checkResultIDs(results []Result) []string {
	ids := make([]string, 0, len(results))
	for _, result := range results {
		ids = append(ids, result.ID)
	}
	return ids
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for index := range a {
		if a[index] != b[index] {
			return false
		}
	}
	return true
}
