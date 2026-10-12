package agenttool_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pulseaiclub/phi/internal/job"
	"github.com/pulseaiclub/phi/internal/tools"
)

func TestChainInterruptedWaitPreservesJobStatus(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	mgr, err := job.New(
		job.Options{
			Root: t.TempDir(),
			Runner: job.RunnerFunc(func(runCtx context.Context, _ job.RunEnv) (string, error) {
				cancel()
				<-runCtx.Done()
				return "", runCtx.Err()
			}),
		},
	)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, mgr.Close(context.WithoutCancel(t.Context()))) })
	reg := tools.NewRegistry(tools.AgentTools(tools.AgentDeps{Manager: mgr}))
	result, err := reg["agent_spawn"].Run(
		ctx,
		json.RawMessage(`{"chain":[{"task":"first"},{"task":"must not start"}]}`),
	)
	require.NoError(t, err)
	var out struct {
		Chain []struct {
			JobID  string     `json:"job_id"`
			Status job.Status `json:"status"`
			Error  string     `json:"error"`
		} `json:"chain"`
		FailedAt int `json:"failed_at"`
	}
	require.NoError(t, json.Unmarshal([]byte(result.Content), &out))
	require.Len(t, out.Chain, 1)
	assert.Equal(t, 0, out.FailedAt)
	assert.Equal(t, job.StatusRunning, out.Chain[0].Status)
	assert.NotEmpty(t, out.Chain[0].Error)
	info, err := mgr.Get(t.Context(), out.Chain[0].JobID)
	require.NoError(t, err)
	assert.Equal(t, info.Status, out.Chain[0].Status)
}
