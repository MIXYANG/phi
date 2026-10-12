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
	"github.com/pulseaiclub/phi/internal/tools/tooldef"
)

func steeringFixture(t *testing.T, runner job.RunnerFunc) (*job.Manager, tooldef.Tool, string) {
	t.Helper()
	started := make(chan struct{})
	mgr, err := job.New(
		job.Options{
			Root: t.TempDir(),
			Runner: job.RunnerFunc(func(ctx context.Context, env job.RunEnv) (string, error) {
				close(started)
				return runner(ctx, env)
			}),
		},
	)
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Close(context.WithoutCancel(t.Context())) })
	info, err := mgr.Spawn(t.Context(), job.SpawnRequest{Prompt: "work"})
	require.NoError(t, err)
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		require.FailNow(t, "runner did not start")
	}
	reg := tools.NewRegistry(tools.AgentTools(tools.AgentDeps{Manager: mgr}))
	return mgr, reg["agent_wait"], info.ID
}

func TestWaitSteeringQueuedThenDelivered(t *testing.T) {
	release := make(chan struct{})
	received := make(chan []string, 1)
	mgr, wait, id := steeringFixture(t, func(ctx context.Context, env job.RunEnv) (string, error) {
		select {
		case <-release:
		case <-ctx.Done():
			return "", ctx.Err()
		}
		messages := env.DrainSteering(false)
		received <- messages
		env.AckSteering(len(messages))
		return "steered summary", nil
	})
	raw, err := json.Marshal(map[string]any{"job_id": id, "message": "focus tests", "timeout_sec": 0})
	require.NoError(t, err)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	res, err := wait.Run(ctx, raw)
	require.NoError(t, err)
	assert.JSONEq(
		t,
		mustMarshalTest(t, map[string]any{"job_id": id, "queued": true, "injected_messages": 0}),
		res.Content,
	)
	assert.Equal(t, "message queued", res.Detail)
	assert.Zero(t, mgr.InjectedMessages(id))
	close(release)
	for _, input := range []map[string]any{{"job_id": id, "timeout_sec": 0}, {"job_ids": []string{id}}} {
		raw, err = json.Marshal(input)
		require.NoError(t, err)
		res, err = wait.Run(t.Context(), raw)
		require.NoError(t, err)
		assert.Contains(t, res.Content, `"injected_messages": 1`)
		assert.Contains(t, res.Content, "steered summary")
	}
	assert.Equal(t, []string{"focus tests"}, <-received)
	raw, err = json.Marshal(map[string]any{"job_id": id, "message": "too late"})
	require.NoError(t, err)
	_, err = wait.Run(t.Context(), raw)
	require.ErrorIs(t, err, job.ErrNotRunning)
}

func TestWaitSteeringPositiveTimeout(t *testing.T) {
	for _, complete := range []bool{true, false} {
		t.Run(map[bool]string{true: "completion", false: "timeout"}[complete], func(t *testing.T) {
			_, wait, id := steeringFixture(t, func(ctx context.Context, env job.RunEnv) (string, error) {
				ticker := time.NewTicker(time.Millisecond)
				defer ticker.Stop()
				for {
					select {
					case <-ctx.Done():
						return "", ctx.Err()
					case <-ticker.C:
						messages := env.DrainSteering(false)
						if len(messages) == 0 {
							continue
						}
						env.AckSteering(len(messages))
						if complete {
							return messages[0], nil
						}
						<-ctx.Done()
						return "", ctx.Err()
					}
				}
			})
			raw, err := json.Marshal(map[string]any{"job_id": id, "message": "new direction", "timeout_sec": 1})
			require.NoError(t, err)
			res, err := wait.Run(t.Context(), raw)
			require.NoError(t, err)
			assert.Contains(t, res.Content, `"injected_messages": 1`)
			if complete {
				assert.Contains(t, res.Content, `"status": "completed"`)
				assert.Contains(t, res.Content, "new direction")
			} else {
				assert.Contains(t, res.Content, `"status": "running"`)
				raw, err = json.Marshal(map[string]any{"job_ids": []string{id}, "timeout_sec": 1})
				require.NoError(t, err)
				res, err = wait.Run(t.Context(), raw)
				require.NoError(t, err)
				var batch struct {
					Pending []string          `json:"pending"`
					Results []json.RawMessage `json:"results"`
				}
				require.NoError(t, json.Unmarshal([]byte(res.Content), &batch))
				assert.Equal(t, []string{id}, batch.Pending)
				assert.Empty(t, batch.Results)
			}
		})
	}
}

func TestWaitSteeringValidation(t *testing.T) {
	_, wait, id := steeringFixture(t, func(ctx context.Context, _ job.RunEnv) (string, error) {
		<-ctx.Done()
		return "", ctx.Err()
	})
	for _, input := range []map[string]any{
		{"message": "hello"},
		{"job_id": id, "job_ids": []string{id}, "message": "hello"},
		{"job_id": id, "job_ids": []string{}, "message": "hello"},
		{"job_id": id, "message": ""},
		{"job_id": id, "message": "  "},
		{"job_id": "missing", "message": "hello"},
		{"job_id": id, "message": "hello", "timeout_sec": -1},
		{"job_id": id, "timeout_sec": -1},
		{"job_ids": []string{id}, "timeout_sec": -1},
	} {
		raw, err := json.Marshal(input)
		require.NoError(t, err)
		_, err = wait.Run(t.Context(), raw)
		assert.Error(t, err, string(raw))
	}
	require.Contains(t, wait.Definition.Params.Properties, "message")
}

func mustMarshalTest(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	require.NoError(t, err)
	return string(raw)
}
