package agent

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pulseaiclub/phi/internal/job"
	"github.com/pulseaiclub/phi/internal/llm"
	"github.com/pulseaiclub/phi/internal/permission"
	"github.com/pulseaiclub/phi/internal/tools"
)

func TestEngineRunnerCountsUsageAtToolBudgetLimit(t *testing.T) {
	var requests, runs atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		n := requests.Add(1)
		_, _ = fmt.Fprint(w, sseToolCallChunk(fmt.Sprintf("call_%d", n), "count", `{}`))
		_, _ = fmt.Fprintf(w,
			"data: {\"choices\":[],\"usage\":{\"prompt_tokens\":%d,\"completion_tokens\":%d,\"total_tokens\":%d}}\n\n",
			10*n, 2*n, 12*n,
		)
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	runner := EngineRunner{
		Model:     llm.ModelConfig{Name: "fake", BaseURL: server.URL, APIKey: "x"},
		MaxRounds: 1,
		Gate:      permission.AllowAll{},
		Tools:     []tools.Tool{countingTool(&runs)},
	}
	dir := t.TempDir()
	var usage []job.UsageDelta
	_, err := runner.Run(t.Context(), job.RunEnv{
		Job: job.Meta{Dir: dir, WorkDir: t.TempDir(), Prompt: "go"},
		Log: func(string) {},
		OnProgress: func(progress job.Progress) {
			if progress.Usage != nil {
				usage = append(usage, *progress.Usage)
			}
		},
	})
	require.ErrorIs(t, err, ErrMaxRounds)
	require.EqualValues(t, 2, requests.Load())
	require.EqualValues(t, 1, runs.Load())
	require.Equal(t, []job.UsageDelta{
		{InputTokens: 10, OutputTokens: 2},
		{InputTokens: 20, OutputTokens: 4},
	}, usage)

	files, err := filepath.Glob(filepath.Join(dir, "session", "*.jsonl"))
	require.NoError(t, err)
	require.Len(t, files, 1)
	raw, err := os.ReadFile(files[0])
	require.NoError(t, err)
	require.Contains(t, string(raw), "call_1")
	require.NotContains(t, string(raw), "call_2")
}
