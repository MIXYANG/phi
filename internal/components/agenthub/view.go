package agenthub

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// StatusIcon returns a Unicode glyph for a job status.
func StatusIcon(s string) string {
	switch s {
	case "running", "starting":
		return "⠋"
	case "completed":
		return "✓"
	case "failed":
		return "✗"
	case "cancelled":
		return "⊘"
	case "timed_out":
		return "⏱"
	default:
		return "·"
	}
}

// FormatElapsed renders a duration in compact form.
func FormatElapsed(d time.Duration) string {
	if d < 0 {
		return "0s"
	}
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm%ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
}

// FormatUsage renders all reported token buckets and cost.
func FormatUsage(u UsageSummary) string {
	var parts []string
	if u.InputTokens > 0 {
		parts = append(parts, "↓"+fmtTokens(u.InputTokens))
	}
	if u.OutputTokens > 0 {
		parts = append(parts, "↑"+fmtTokens(u.OutputTokens))
	}
	if u.CacheRead > 0 {
		parts = append(parts, "⚡"+fmtTokens(u.CacheRead))
	}
	if u.CacheWrite > 0 {
		parts = append(parts, "cache-write:"+fmtTokens(u.CacheWrite))
	}
	if u.CostUSD > 0 {
		parts = append(parts, fmt.Sprintf("$%.4f", u.CostUSD))
	}
	return strings.Join(parts, " ")
}

func fmtTokens(n int) string {
	if n < 1000 {
		return strconv.Itoa(n)
	}
	if n < 1000000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprintf("%.1fM", float64(n)/1000000)
}

// RowLine renders one row; the surface clips at display-column boundaries.
func RowLine(r AgentRow, spinnerGlyph string) string {
	icon := StatusIcon(string(r.Status))
	if !r.Status.Terminal() {
		icon = spinnerGlyph
	}
	role := string(r.Role)
	if role == "" {
		role = "explore"
	}
	parts := []string{icon, role, string(r.Status), FormatElapsed(r.Elapsed)}
	if usage := FormatUsage(r.Usage); usage != "" {
		parts = append(parts, usage)
	}
	if r.Detail != "" {
		parts = append(parts, strings.Join(strings.Fields(r.Detail), " "))
	}
	return strings.Join(parts, " ")
}

// Summary renders lifecycle counts and authoritative total usage.
func Summary(rows []AgentRow) string {
	counts := make(map[string]int)
	var u UsageSummary
	for _, r := range rows {
		counts[string(r.Status)]++
		u.InputTokens += r.Usage.InputTokens
		u.OutputTokens += r.Usage.OutputTokens
		u.CacheRead += r.Usage.CacheRead
		u.CacheWrite += r.Usage.CacheWrite
		u.CostUSD += r.Usage.CostUSD
	}
	parts := []string{fmt.Sprintf("%d running", counts["running"]), fmt.Sprintf("%d completed", counts["completed"])}
	for _, s := range []string{"starting", "failed", "cancelled", "timed_out"} {
		if counts[s] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", counts[s], s))
		}
	}
	parts = append(parts, fmt.Sprintf("%d total", len(rows)))
	if usage := FormatUsage(u); usage != "" {
		parts = append(parts, usage)
	}
	if u.CostUSD == 0 {
		parts = append(parts, "cost: unavailable")
	}
	return strings.Join(parts, " · ")
}

// Render produces the hub text.
func Render(rows []AgentRow, spinnerGlyph string) string {
	if len(rows) == 0 {
		return "No sub-agents for this session."
	}
	var lines []string
	for _, r := range rows {
		lines = append(lines, RowLine(r, spinnerGlyph))
	}
	return strings.Join(append(lines, Summary(rows)), "\n")
}

// PinnedBar renders the live roles and statuses above the composer.
func PinnedBar(rows []AgentRow, spinnerGlyph string) string {
	var parts []string
	for _, r := range rows {
		if r.Status.Terminal() {
			continue
		}
		role := string(r.Role)
		if role == "" {
			role = "explore"
		}
		parts = append(parts, spinnerGlyph+" "+role+" "+string(r.Status))
	}
	if len(parts) == 0 {
		return ""
	}
	return "Alt+A · " + strings.Join(parts, "  ")
}
