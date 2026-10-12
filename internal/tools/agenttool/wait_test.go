package agenttool_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pulseaiclub/phi/internal/job"
	"github.com/pulseaiclub/phi/internal/tools"
)

func TestWaitBatchTimeoutKeepsTerminalSummariesAndErrors(t *testing.T) {
	mgr, err := job.New(job.Options{
		Root: t.TempDir(),
		Runner: job.RunnerFunc(func(ctx context.Context, env job.RunEnv) (string, error) {
			if env.Job.Prompt == "done" {
				return "terminal summary", nil
			}
			<-ctx.Done()
			return "", ctx.Err()
		}),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Close(context.WithoutCancel(t.Context())) })
	spawn := func(prompt string) string {
		info, spawnErr := mgr.Spawn(t.Context(), job.SpawnRequest{Prompt: prompt})
		require.NoError(t, spawnErr)
		return info.ID
	}
	first := spawn("blocked first")
	done := spawn("done")
	_, err = mgr.Wait(t.Context(), done)
	require.NoError(t, err)
	second := spawn("blocked second")
	reg := tools.NewRegistry(tools.AgentTools(tools.AgentDeps{Manager: mgr}))
	raw, err := json.Marshal(
		map[string]any{"job_ids": []string{first, done, "missing", "../bad", second}, "timeout_sec": 1},
	)
	require.NoError(t, err)
	start := time.Now()
	res, err := reg["agent_wait"].Run(t.Context(), raw)
	require.NoError(t, err)
	assert.Less(t, time.Since(start), 1800*time.Millisecond, "batch uses one timeout budget")
	var out struct {
		Results []struct {
			JobID   string `json:"job_id"`
			Status  string `json:"status"`
			Summary string `json:"summary"`
			Error   string `json:"error"`
		} `json:"results"`
		Pending []string `json:"pending"`
	}
	require.NoError(t, json.Unmarshal([]byte(res.Content), &out))
	require.Len(t, out.Results, 3)
	assert.Equal(t, []string{first, second}, out.Pending)
	assert.Equal(t, "completed", out.Results[0].Status)
	assert.Equal(t, "terminal summary", out.Results[0].Summary)
	for _, index := range []int{1, 2} {
		assert.Equal(t, "error", out.Results[index].Status)
		assert.NotEmpty(t, out.Results[index].Error)
	}
}
