package precheck

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
)

// CheckMetadata is the operator-facing description of a registered check.
// Keeping this separate from Check avoids exposing implementation callbacks in
// discovery output.
type CheckMetadata struct {
	ID    string `json:"id"`
	Scope string `json:"scope"`
	Name  string `json:"name"`
}

// CheckList is the versioned, offline output returned by --list-checks.
type CheckList struct {
	SchemaVersion string          `json:"schemaVersion"`
	Checks        []CheckMetadata `json:"checks"`
}

// ListChecks returns check metadata in canonical registry order.
func ListChecks() []CheckMetadata {
	registered := Registry()
	metadata := make([]CheckMetadata, 0, len(registered))
	for _, check := range registered {
		metadata = append(metadata, CheckMetadata{ID: check.ID, Scope: check.Scope, Name: check.Name})
	}
	return metadata
}

// SelectChecks validates a requested set of IDs and returns the corresponding
// checks in registry order. A nil selection means all registered checks. IDs
// are case-sensitive; whitespace is ignored and duplicates are collapsed.
func SelectChecks(ids []string) ([]Check, error) {
	registered := Registry()
	if ids == nil {
		return registered, nil
	}

	byID := make(map[string]Check, len(registered))
	validIDs := make([]string, 0, len(registered))
	for _, check := range registered {
		byID[check.ID] = check
		validIDs = append(validIDs, check.ID)
	}

	wanted := make(map[string]struct{}, len(ids))
	for _, rawID := range ids {
		id := strings.TrimSpace(rawID)
		if id == "" {
			return nil, fmt.Errorf("--checks contains an empty check ID")
		}
		if _, ok := byID[id]; !ok {
			return nil, fmt.Errorf("unknown check ID %q (valid IDs: %s)", id, strings.Join(validIDs, ", "))
		}
		wanted[id] = struct{}{}
	}

	selected := make([]Check, 0, len(wanted))
	for _, check := range registered {
		if _, ok := wanted[check.ID]; ok {
			selected = append(selected, check)
		}
	}
	return selected, nil
}

// WriteCheckList emits the stable human-readable or versioned JSON check
// catalog used by --list-checks.
func WriteCheckList(w io.Writer, metadata []CheckMetadata, format string) error {
	if format == "json" {
		encoder := json.NewEncoder(w)
		encoder.SetIndent("", "  ")
		return encoder.Encode(CheckList{SchemaVersion: "v1", Checks: metadata})
	}

	table := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(table, "ID\tSCOPE\tDISPLAY NAME"); err != nil {
		return err
	}
	for _, check := range metadata {
		if _, err := fmt.Fprintf(table, "%s\t%s\t%s\n", check.ID, check.Scope, check.Name); err != nil {
			return err
		}
	}
	return table.Flush()
}
