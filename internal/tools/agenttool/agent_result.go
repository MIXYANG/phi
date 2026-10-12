package agenttool

import (
	"encoding/json"
	"strings"

	"github.com/pulseaiclub/phi/internal/job"
)

// AgentResult is the UI-facing parse of agent_spawn / agent_wait JSON output.
type AgentResult struct {
	JobID   string
	Status  string
	Summary string
	Error   string
	OK      bool          // true when output looked like agent tool JSON
	Jobs    []AgentResult // individual results from orchestration envelopes
}

// ParseAgentResult extracts job fields from tool Output/Content JSON.
// Non-JSON or unrelated payloads return OK=false.
func ParseAgentResult(output string) AgentResult {
	output = strings.TrimSpace(output)
	if output == "" || output[0] != '{' {
		return AgentResult{}
	}
	var raw struct {
		JobID   string            `json:"job_id"`
		Status  string            `json:"status"`
		Summary string            `json:"summary"`
		Error   string            `json:"error"`
		Jobs    []json.RawMessage `json:"jobs"`
		Results []json.RawMessage `json:"results"`
		Chain   []json.RawMessage `json:"chain"`
	}
	if err := json.Unmarshal([]byte(output), &raw); err != nil {
		return AgentResult{}
	}
	entries := raw.Jobs
	if raw.Results != nil {
		entries = raw.Results
	}
	if raw.Chain != nil {
		entries = raw.Chain
	}
	if entries != nil {
		result := AgentResult{OK: true}
		for _, entry := range entries {
			child := ParseAgentResult(string(entry))
			if child.OK {
				result.Jobs = append(result.Jobs, child)
			}
		}
		if raw.Chain != nil && len(result.Jobs) > 0 {
			last := result.Jobs[len(result.Jobs)-1]
			result.Status, result.Summary, result.Error = last.Status, raw.Summary, last.Error
		}
		return result
	}
	if raw.JobID == "" && raw.Status == "" && raw.Summary == "" && raw.Error == "" {
		return AgentResult{}
	}
	return AgentResult{
		JobID:   raw.JobID,
		Status:  raw.Status,
		Summary: raw.Summary,
		Error:   raw.Error,
		OK:      true,
	}
}

// Terminal reports whether Status is a finished job status.
func (r AgentResult) Terminal() bool {
	return job.Status(r.Status).Terminal()
}

// RenderableSummary is the markdown body to show when the job finished with a summary.
func (r AgentResult) RenderableSummary() string {
	if !r.OK {
		return ""
	}
	if r.Terminal() {
		return strings.TrimSpace(r.Summary)
	}
	var summaries []string
	for _, child := range r.Jobs {
		if summary := child.RenderableSummary(); summary != "" {
			summaries = append(summaries, "### "+child.JobID+"\n\n"+summary)
		}
	}
	return strings.Join(summaries, "\n\n")
}
