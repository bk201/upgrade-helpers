package precheck

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

type Status string

const (
	Pass    Status = "PASS"
	Warning Status = "WARNING"
	Fail    Status = "FAIL"
	Skipped Status = "SKIPPED"
	Error   Status = "ERROR"
)

type NodeResult struct {
	Node    string   `json:"node"`
	Status  Status   `json:"status"`
	Summary string   `json:"summary"`
	Details []string `json:"details,omitempty"`
}

type Result struct {
	ID             string       `json:"id"`
	Name           string       `json:"name"`
	Scope          string       `json:"scope"`
	Status         Status       `json:"status"`
	Summary        string       `json:"summary"`
	Details        []string     `json:"details,omitempty"`
	Nodes          []NodeResult `json:"nodes,omitempty"`
	DurationMillis int64        `json:"durationMillis"`
}

type Summary struct {
	Pass    int `json:"pass"`
	Warning int `json:"warning"`
	Fail    int `json:"fail"`
	Skipped int `json:"skipped"`
	Error   int `json:"error"`
}

type Report struct {
	SchemaVersion  string    `json:"schemaVersion"`
	ClusterVersion string    `json:"clusterVersion"`
	StartedAt      time.Time `json:"startedAt"`
	FinishedAt     time.Time `json:"finishedAt"`
	Summary        Summary   `json:"summary"`
	Checks         []Result  `json:"checks"`
}

func NewResult(status Status, summary string, details ...string) Result {
	return Result{Status: status, Summary: summary, Details: compact(details)}
}

func (r Report) Failed() bool { return r.Summary.Fail > 0 || r.Summary.Error > 0 }

func Summarize(results []Result) Summary {
	var s Summary
	for _, result := range results {
		switch result.Status {
		case Pass:
			s.Pass++
		case Warning:
			s.Warning++
		case Fail:
			s.Fail++
		case Skipped:
			s.Skipped++
		case Error:
			s.Error++
		}
	}
	return s
}

func worstStatus(statuses ...Status) Status {
	rank := map[Status]int{Skipped: 0, Pass: 1, Warning: 2, Fail: 3, Error: 4}
	worst := Skipped
	for _, status := range statuses {
		if rank[status] > rank[worst] {
			worst = status
		}
	}
	return worst
}

func validStatus(status Status) bool {
	switch status {
	case Pass, Warning, Fail, Skipped, Error:
		return true
	default:
		return false
	}
}

func compact(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
	}
	return out
}

func WriteReport(w io.Writer, report Report, format string) error {
	if format == "json" {
		encoder := json.NewEncoder(w)
		encoder.SetIndent("", "  ")
		return encoder.Encode(report)
	}

	if _, err := fmt.Fprintf(w, "Harvester upgrade pre-check (cluster %s)\n\n", report.ClusterVersion); err != nil {
		return err
	}
	for _, result := range report.Checks {
		if _, err := fmt.Fprintf(w, "[%s] %s: %s\n", result.Status, result.Name, result.Summary); err != nil {
			return err
		}
		for _, detail := range result.Details {
			if _, err := fmt.Fprintf(w, "  - %s\n", detail); err != nil {
				return err
			}
		}
		for _, node := range result.Nodes {
			if _, err := fmt.Fprintf(w, "  [%s] %s: %s\n", node.Status, node.Node, node.Summary); err != nil {
				return err
			}
			for _, detail := range node.Details {
				if _, err := fmt.Fprintf(w, "    - %s\n", detail); err != nil {
					return err
				}
			}
		}
		if _, err := fmt.Fprintln(w); err != nil {
			return err
		}
	}
	_, err := fmt.Fprintf(w, "Summary: %d passed, %d warning, %d failed, %d skipped, %d error\n",
		report.Summary.Pass, report.Summary.Warning, report.Summary.Fail, report.Summary.Skipped, report.Summary.Error)
	return err
}

type Check struct {
	ID    string
	Name  string
	Scope string
	Run   func(context.Context, *Environment) Result
}

func stableNodeResults(results []NodeResult) {
	sort.Slice(results, func(i, j int) bool { return results[i].Node < results[j].Node })
}
