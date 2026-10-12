package agenttool_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pulseaiclub/phi/internal/tools"
)

func TestParseAgentResultSummary(t *testing.T) {
	out := `{
  "job_id": "job_1",
  "status": "completed",
  "error": "",
  "summary": "## Findings\n\n- ok\n"
}`
	r := tools.ParseAgentResult(out)
	require.True(t, r.OK)
	require.Equal(t, "job_1", r.JobID)
	require.Equal(t, "completed", r.Status)
	require.Equal(t, "## Findings\n\n- ok", r.RenderableSummary())
}

func TestParseAgentResultRunningNoSummary(t *testing.T) {
	r := tools.ParseAgentResult(`{"job_id":"j","status":"running"}`)
	require.True(t, r.OK)
	require.Empty(t, r.RenderableSummary())
}

func TestParseAgentResultRejectsPlain(t *testing.T) {
	assert.False(t, tools.ParseAgentResult("hello").OK, "expected reject")
}

func TestParseAgentResultOrchestration(t *testing.T) {
	for _, tc := range []struct {
		name, output, summary string
		jobs                  int
	}{
		{"parallel", `{"jobs":[{"job_id":"a","status":"starting"},{"error":"busy"}]}`, "", 2},
		{"batch", `{"results":[{"job_id":"a","status":"completed","summary":"found it"}],"pending":["b"]}`, "### a\n\nfound it", 1},
		{"chain", `{"chain":[{"job_id":"a","status":"completed","summary":"first"},{"job_id":"b","status":"completed","summary":"last"}],"summary":"last","failed_at":-1}`, "last", 2},
		{"failed chain", `{"chain":[{"job_id":"a","status":"failed","summary":"partial","error":"failed"}],"summary":"partial","failed_at":0}`, "partial", 1},
		{"pending only", `{"results":[],"pending":["a"]}`, "", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := tools.ParseAgentResult(tc.output)
			require.True(t, r.OK)
			assert.Len(t, r.Jobs, tc.jobs)
			assert.Equal(t, tc.summary, r.RenderableSummary())
		})
	}
}
