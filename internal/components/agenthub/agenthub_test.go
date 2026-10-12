package agenthub_test

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"

	"github.com/pulseaiclub/phi/internal/components/agenthub"
)

func TestFormatting(t *testing.T) {
	assert.Equal(t, "1m23s", agenthub.FormatElapsed(83*time.Second))
	assert.Empty(t, agenthub.FormatUsage(agenthub.UsageSummary{}))
	assert.Equal(
		t,
		"↓1.5k ↑200 cache-write:42 $0.1200",
		agenthub.FormatUsage(agenthub.UsageSummary{InputTokens: 1500, OutputTokens: 200, CacheWrite: 42, CostUSD: .12}),
	)
	detail := strings.Repeat("你好🌍", 30)
	line := agenthub.RowLine(agenthub.AgentRow{Role: "worker", Status: "completed", Detail: detail}, "⠋")
	assert.True(t, utf8.ValidString(line))
	assert.Contains(t, line, detail)
	assert.Contains(t, line, "✓ worker completed")
}

func TestLifecycleRendering(t *testing.T) {
	assert.Equal(t, "No sub-agents for this session.", agenthub.Render(nil, "⠋"))
	rows := []agenthub.AgentRow{
		{Role: "explore", Status: "starting"},
		{Role: "worker", Status: "completed"},
		{Role: "review", Status: "failed"},
		{Status: "cancelled"},
		{Status: "timed_out"},
	}
	out := agenthub.Render(rows, "⠙")
	for _, s := range []string{"1 starting", "1 completed", "1 failed", "1 cancelled", "1 timed_out", "5 total"} {
		assert.Contains(t, out, s)
	}
	bar := agenthub.PinnedBar(rows, "⠙")
	assert.Contains(t, bar, "explore starting")
	assert.NotContains(t, bar, "worker")
	assert.Empty(t, agenthub.PinnedBar(rows[1:], "⠙"))
}
