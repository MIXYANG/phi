package job

import "time"

// Progress is a structured live update from a running job (UI overlay, not LLM context).
type Progress struct {
	JobID           string
	ParentToolUseID string // parent agent tool_use id when known
	ToolUseID       string
	Name            string
	Status          string // tool status, or job lifecycle status when ToolUseID and Name are empty
	Detail          string
	Time            time.Time
	Usage           *UsageDelta // non-nil when this event carries token usage
}

// UsageDelta is a token-usage increment for one completion round.
type UsageDelta struct {
	InputTokens  int
	OutputTokens int
	CacheRead    int
	CacheWrite   int
	CostUSD      float64 // zero when pricing is unavailable
}
