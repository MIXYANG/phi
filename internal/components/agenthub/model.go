// Package agenthub renders sub-agent snapshots supplied by the TUI.
package agenthub

import (
	"time"

	"github.com/pulseaiclub/phi/internal/job"
)

// UsageSummary is a UI-facing token/cost snapshot for one agent row.
type UsageSummary struct {
	InputTokens  int
	OutputTokens int
	CacheRead    int
	CacheWrite   int
	CostUSD      float64
}

// Total returns the gross token count (all buckets).
func (u UsageSummary) Total() int { return u.InputTokens + u.OutputTokens + u.CacheRead + u.CacheWrite }

// AgentRow is one line in the Agent Hub table.
type AgentRow struct {
	JobID     string
	Role      job.Role
	Status    job.Status
	Detail    string
	Elapsed   time.Duration
	Usage     UsageSummary
	ParentID  string
	CreatedAt time.Time
}
