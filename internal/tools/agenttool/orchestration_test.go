package agenttool_test

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pulseaiclub/phi/internal/job"
	"github.com/pulseaiclub/phi/internal/tools"
)

// --- Parallel tasks ---

func TestSpawnTasksParallel(t *testing.T) {
	var count atomic.Int32
	mgr, err := job.New(job.Options{
		Root: t.TempDir(),
		Runner: job.RunnerFunc(func(_ context.Context, env job.RunEnv) (string, error) {
			count.Add(1)
			return "done:" + env.Job.Description, nil
		}),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Close(t.Context()) })

	reg := tools.NewRegistry(tools.AgentTools(tools.AgentDeps{
		Manager:  mgr,
		ParentID: func() string { return "p" },
		WorkDir:  func() string { return t.TempDir() },
	}))

	raw, _ := json.Marshal(map[string]any{
		"tasks": []map[string]any{
			{"role": "explore", "task": "find auth", "description": "A"},
			{"role": "review", "task": "check diff", "description": "B"},
			{"role": "worker", "task": "fix bug", "description": "C"},
		},
	})
	res, err := reg["agent_spawn"].Run(t.Context(), raw)
	require.NoError(t, err)
	assert.Equal(t, "parallel · 3 tasks", res.Detail)

	var out struct {
		Jobs []struct {
			JobID  string `json:"job_id"`
			Role   string `json:"role"`
			Status string `json:"status"`
			Dir    string `json:"dir"`
			Error  string `json:"error"`
		} `json:"jobs"`
	}
	require.NoError(t, json.Unmarshal([]byte(res.Content), &out))
	require.Len(t, out.Jobs, 3)

	for _, j := range out.Jobs {
		assert.Empty(t, j.Error, "job %s should not have error", j.JobID)
		assert.Equal(t, "starting", j.Status)
		assert.NotEmpty(t, j.JobID)
		assert.DirExists(t, j.Dir)
	}

	// Wait for all and verify summaries.
	for _, j := range out.Jobs {
		waitRaw, _ := json.Marshal(map[string]any{"job_id": j.JobID})
		waitRes, err := reg["agent_wait"].Run(t.Context(), waitRaw)
		require.NoError(t, err)
		assert.Contains(t, waitRes.Content, `"status": "completed"`)
	}
	assert.Equal(t, int32(3), count.Load())
}

func TestSpawnTasksPartialFailure(t *testing.T) {
	release := make(chan struct{})
	mgr, err := job.New(job.Options{
		Root:          t.TempDir(),
		MaxConcurrent: 1,
		Runner: job.RunnerFunc(func(ctx context.Context, _ job.RunEnv) (string, error) {
			select {
			case <-release:
				return "ok", nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Close(t.Context()) })

	reg := tools.NewRegistry(tools.AgentTools(tools.AgentDeps{
		Manager:  mgr,
		ParentID: func() string { return "p" },
		WorkDir:  func() string { return t.TempDir() },
	}))

	// 3 tasks but MaxConcurrent=1, so 2nd and 3rd should get ErrBusy.
	raw, _ := json.Marshal(map[string]any{
		"tasks": []map[string]any{
			{"task": "first"},
			{"task": "second"},
			{"task": "third"},
		},
	})
	res, err := reg["agent_spawn"].Run(t.Context(), raw)
	require.NoError(t, err)

	var out struct {
		Jobs []struct {
			JobID string `json:"job_id"`
			Error string `json:"error"`
		} `json:"jobs"`
	}
	require.NoError(t, json.Unmarshal([]byte(res.Content), &out))
	require.Len(t, out.Jobs, 3)

	// First should succeed, rest should be busy.
	require.NotEmpty(t, out.Jobs[0].JobID)
	assert.Empty(t, out.Jobs[0].Error)
	for _, entry := range out.Jobs[1:] {
		assert.Empty(t, entry.JobID)
		assert.Contains(t, entry.Error, job.ErrBusy.Error())
	}
	close(release)

	// Wait for the running job so TempDir cleanup succeeds.
	waitRaw, _ := json.Marshal(map[string]any{"job_id": out.Jobs[0].JobID})
	_, err = reg["agent_wait"].Run(t.Context(), waitRaw)
	require.NoError(t, err)
}

func TestSpawnTasksAndChainMutuallyExclusive(t *testing.T) {
	mgr, err := job.New(job.Options{
		Root: t.TempDir(),
		Runner: job.RunnerFunc(func(_ context.Context, _ job.RunEnv) (string, error) {
			return "x", nil
		}),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Close(t.Context()) })

	reg := tools.NewRegistry(tools.AgentTools(tools.AgentDeps{
		Manager:  mgr,
		ParentID: func() string { return "p" },
	}))

	raw, _ := json.Marshal(map[string]any{
		"tasks": []map[string]any{{"task": "a"}},
		"chain": []map[string]any{{"task": "b"}},
	})
	_, err = reg["agent_spawn"].Run(t.Context(), raw)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mutually exclusive")
}

// --- Chain ---

func TestSpawnChainSequential(t *testing.T) {
	var mu sync.Mutex
	var order []string

	mgr, err := job.New(job.Options{
		Root: t.TempDir(),
		Runner: job.RunnerFunc(func(_ context.Context, env job.RunEnv) (string, error) {
			mu.Lock()
			order = append(order, env.Job.Prompt)
			mu.Unlock()
			return "result:" + env.Job.Description, nil
		}),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Close(t.Context()) })

	reg := tools.NewRegistry(tools.AgentTools(tools.AgentDeps{
		Manager:  mgr,
		ParentID: func() string { return "p" },
		WorkDir:  func() string { return t.TempDir() },
	}))

	raw, _ := json.Marshal(map[string]any{
		"chain": []map[string]any{
			{"role": "explore", "task": "step one", "description": "S1"},
			{"role": "worker", "task": "step two: {previous}", "description": "S2"},
			{"role": "review", "task": "final: {previous}", "description": "S3"},
		},
	})
	res, err := reg["agent_spawn"].Run(t.Context(), raw)
	require.NoError(t, err)
	assert.Equal(t, "chain · 3 steps", res.Detail)

	var out struct {
		Chain []struct {
			JobID   string `json:"job_id"`
			Role    string `json:"role"`
			Status  string `json:"status"`
			Summary string `json:"summary"`
		} `json:"chain"`
		FailedAt int    `json:"failed_at"`
		Summary  string `json:"summary"`
	}
	require.NoError(t, json.Unmarshal([]byte(res.Content), &out))
	require.Len(t, out.Chain, 3)
	assert.Equal(t, -1, out.FailedAt)

	for _, step := range out.Chain {
		assert.Equal(t, "completed", step.Status)
		assert.NotEmpty(t, step.JobID)
	}

	// Verify {previous} substitution.
	mu.Lock()
	defer mu.Unlock()
	require.Len(t, order, 3)
	assert.Equal(t, "step one", order[0])
	assert.Equal(t, "step two: result:S1", order[1])
	assert.Equal(t, "final: result:S2", order[2])
}

func TestSpawnChainStopsOnFailure(t *testing.T) {
	callCount := 0
	mgr, err := job.New(job.Options{
		Root: t.TempDir(),
		Runner: job.RunnerFunc(func(_ context.Context, env job.RunEnv) (string, error) {
			callCount++
			if callCount == 2 {
				return "", assert.AnError
			}
			return "ok:" + env.Job.Description, nil
		}),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Close(t.Context()) })

	reg := tools.NewRegistry(tools.AgentTools(tools.AgentDeps{
		Manager:  mgr,
		ParentID: func() string { return "p" },
		WorkDir:  func() string { return t.TempDir() },
	}))

	raw, _ := json.Marshal(map[string]any{
		"chain": []map[string]any{
			{"task": "first", "description": "A"},
			{"task": "second", "description": "B"},
			{"task": "third", "description": "C"},
		},
	})
	res, err := reg["agent_spawn"].Run(t.Context(), raw)
	require.NoError(t, err)

	var out struct {
		Chain    []struct{ Status string } `json:"chain"`
		FailedAt int                       `json:"failed_at"`
	}
	require.NoError(t, json.Unmarshal([]byte(res.Content), &out))
	assert.Equal(t, 1, out.FailedAt, "should fail at step 2 (index 1)")
	require.Len(t, out.Chain, 2, "third step should not run")
	assert.Equal(t, "completed", out.Chain[0].Status)
	assert.Equal(t, "failed", out.Chain[1].Status)
}

// --- Batch wait ---

func TestWaitBatchMultipleJobs(t *testing.T) {
	mgr, err := job.New(job.Options{
		Root: t.TempDir(),
		Runner: job.RunnerFunc(func(_ context.Context, env job.RunEnv) (string, error) {
			return "summary:" + env.Job.Description, nil
		}),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Close(t.Context()) })

	reg := tools.NewRegistry(tools.AgentTools(tools.AgentDeps{
		Manager:  mgr,
		ParentID: func() string { return "p" },
		WorkDir:  func() string { return t.TempDir() },
	}))

	// Spawn 3 jobs.
	var jobIDs []string
	for _, desc := range []string{"X", "Y", "Z"} {
		raw, _ := json.Marshal(map[string]any{"prompt": "do " + desc, "description": desc})
		res, err := reg["agent_spawn"].Run(t.Context(), raw)
		require.NoError(t, err)
		var out struct {
			JobID string `json:"job_id"`
		}
		require.NoError(t, json.Unmarshal([]byte(res.Content), &out))
		jobIDs = append(jobIDs, out.JobID)
	}

	// Batch wait.
	waitRaw, _ := json.Marshal(map[string]any{"job_ids": jobIDs})
	waitRes, err := reg["agent_wait"].Run(t.Context(), waitRaw)
	require.NoError(t, err)
	assert.Equal(t, "batch · 3 jobs", waitRes.Detail)

	var out struct {
		Results []struct {
			JobID   string `json:"job_id"`
			Status  string `json:"status"`
			Summary string `json:"summary"`
		} `json:"results"`
		Pending []string `json:"pending"`
	}
	require.NoError(t, json.Unmarshal([]byte(waitRes.Content), &out))
	require.Len(t, out.Results, 3)
	assert.Empty(t, out.Pending)

	for i, r := range out.Results {
		assert.Equal(t, "completed", r.Status)
		assert.Contains(t, r.Summary, "summary:")
		assert.Equal(t, jobIDs[i], r.JobID)
	}
}

func TestWaitBatchSingleFallback(t *testing.T) {
	mgr, err := job.New(job.Options{
		Root: t.TempDir(),
		Runner: job.RunnerFunc(func(_ context.Context, _ job.RunEnv) (string, error) {
			return "single-result", nil
		}),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Close(t.Context()) })

	reg := tools.NewRegistry(tools.AgentTools(tools.AgentDeps{
		Manager:  mgr,
		ParentID: func() string { return "p" },
		WorkDir:  func() string { return t.TempDir() },
	}))

	// Spawn one job.
	spawnRaw, _ := json.Marshal(map[string]any{"prompt": "work"})
	spawnRes, err := reg["agent_spawn"].Run(t.Context(), spawnRaw)
	require.NoError(t, err)
	var spawned struct {
		JobID string `json:"job_id"`
	}
	require.NoError(t, json.Unmarshal([]byte(spawnRes.Content), &spawned))

	// Wait with job_id (single mode, backward compat).
	waitRaw, _ := json.Marshal(map[string]any{"job_id": spawned.JobID})
	waitRes, err := reg["agent_wait"].Run(t.Context(), waitRaw)
	require.NoError(t, err)
	assert.Contains(t, waitRes.Content, "single-result")
	assert.Contains(t, waitRes.Content, `"status": "completed"`)
}

// --- Detail function ---

func TestSpawnDetailNewModes(t *testing.T) {
	mgr, err := job.New(job.Options{
		Root: t.TempDir(),
		Runner: job.RunnerFunc(func(_ context.Context, _ job.RunEnv) (string, error) {
			return "x", nil
		}),
	})
	require.NoError(t, err)
	t.Cleanup(func() { _ = mgr.Close(t.Context()) })

	reg := tools.NewRegistry(tools.AgentTools(tools.AgentDeps{
		Manager:  mgr,
		ParentID: func() string { return "p" },
	}))

	detail := reg["agent_spawn"].DetailFromArgs
	require.NotNil(t, detail)

	parallelArgs, _ := json.Marshal(map[string]any{
		"tasks": []map[string]any{{"task": "a"}, {"task": "b"}},
	})
	assert.Equal(t, "parallel · 2 tasks", detail(parallelArgs))

	chainArgs, _ := json.Marshal(map[string]any{
		"chain": []map[string]any{{"task": "a"}, {"task": "b"}, {"task": "c"}},
	})
	assert.Equal(t, "chain · 3 steps", detail(chainArgs))

	// Single mode still works.
	singleArgs, _ := json.Marshal(map[string]any{"prompt": "find auth", "description": "lookup"})
	assert.Equal(t, "explore · lookup", detail(singleArgs))
}
