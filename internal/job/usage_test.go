package job_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pulseaiclub/phi/internal/job"
)

func TestUsageRoundsAndTerminalSnapshots(t *testing.T) {
	for _, status := range []job.Status{
		job.StatusCompleted, job.StatusFailed, job.StatusCancelled, job.StatusTimedOut,
	} {
		t.Run(string(status), func(t *testing.T) {
			ready := make(chan struct{})
			release := make(chan struct{})
			m := newMgr(t, job.RunnerFunc(func(ctx context.Context, env job.RunEnv) (string, error) {
				env.OnProgress(job.Progress{Usage: &job.UsageDelta{
					InputTokens: 100, OutputTokens: 20, CacheRead: 10, CacheWrite: 5, CostUSD: 0.125,
				}})
				env.OnProgress(job.Progress{Usage: &job.UsageDelta{
					InputTokens: 80, OutputTokens: 10, CacheRead: 8, CacheWrite: 3, CostUSD: 0.25,
				}})
				close(ready)
				select {
				case <-ctx.Done():
					return "", ctx.Err()
				case <-release:
					if status == job.StatusFailed {
						return "", errors.New("runner failed")
					}
					return "done", nil
				}
			}), job.Options{})
			req := job.SpawnRequest{Prompt: "usage", ParentID: "parent"}
			if status == job.StatusTimedOut {
				req.Timeout = 200 * time.Millisecond
			}
			info, err := m.Spawn(t.Context(), req)
			require.NoError(t, err)
			select {
			case <-ready:
			case <-time.After(2 * time.Second):
				require.FailNow(t, "runner did not report usage")
			}
			want := job.UsageDelta{InputTokens: 180, OutputTokens: 30, CacheRead: 18, CacheWrite: 8, CostUSD: 0.375}
			assert.Equal(t, want, m.Usage(info.ID))
			snapshot := m.Snapshot("parent")
			require.Len(t, snapshot, 1)
			assert.Equal(t, want, snapshot[0].Usage)
			if status == job.StatusCancelled {
				require.NoError(t, m.Cancel(t.Context(), info.ID))
			} else if status != job.StatusTimedOut {
				close(release)
			}
			result, err := m.Wait(t.Context(), info.ID)
			require.NoError(t, err)
			assert.Equal(t, status, result.Info.Status)
			assert.Equal(t, want, result.Info.Usage)
			assert.Equal(t, want, m.Usage(info.ID))
			snapshot = m.Snapshot("parent")
			require.Len(t, snapshot, 1)
			assert.Equal(t, status, snapshot[0].Status)
			assert.Equal(t, want, snapshot[0].Usage)

			data, err := os.ReadFile(filepath.Join(info.Dir, "meta.json"))
			require.NoError(t, err)
			var meta job.Meta
			require.NoError(t, json.Unmarshal(data, &meta))
			assert.Equal(t, want, meta.Usage)
			assert.Equal(t, status, meta.Status)
		})
	}
}

func TestUsageSnapshotParentIsolationAndSilentLifecycle(t *testing.T) {
	root := t.TempDir()
	m := newMgr(t, job.RunnerFunc(func(context.Context, job.RunEnv) (string, error) {
		return "", nil
	}), job.Options{Root: root})
	progress, cancel := m.Subscribe()
	defer cancel()
	var spawned []job.Info
	for _, parent := range []string{"parent", "parent-other", "parent", ""} {
		info, err := m.Spawn(t.Context(), job.SpawnRequest{
			Prompt: "silent", ParentID: parent, ParentToolUseID: "parent-tool",
		})
		require.NoError(t, err)
		result, err := m.Wait(t.Context(), info.ID)
		require.NoError(t, err)
		spawned = append(spawned, result.Info)
		for _, status := range []job.Status{job.StatusStarting, job.StatusRunning, job.StatusCompleted} {
			select {
			case p := <-progress:
				assert.Equal(t, info.ID, p.JobID)
				assert.Equal(t, string(status), p.Status)
				assert.Empty(t, p.ParentToolUseID)
				assert.Empty(t, p.ToolUseID)
				assert.Empty(t, p.Name)
				assert.Nil(t, p.Usage)
				assert.False(t, p.Time.IsZero())
			case <-time.After(2 * time.Second):
				require.FailNow(t, "missing lifecycle invalidation", "status %s", status)
			}
		}
	}
	reopened := newMgr(t, job.RunnerFunc(func(context.Context, job.RunEnv) (string, error) {
		return "", nil
	}), job.Options{Root: root})
	assert.Empty(t, reopened.Snapshot(""))
	require.NoError(t, os.RemoveAll(root))
	assert.Equal(t, spawned, m.Snapshot(""))
	assert.Equal(t, []job.Info{spawned[0], spawned[2]}, m.Snapshot("parent"))
	assert.Empty(t, m.Snapshot("paren"))
	assert.Zero(t, m.Usage("unknown"))
	assert.Zero(t, m.LiveCount())
	for _, info := range m.Snapshot("") {
		assert.Zero(t, info.Usage)
	}
	copy := m.Snapshot("parent")
	copy[0].Usage.InputTokens = 999
	assert.Zero(t, m.Snapshot("parent")[0].Usage)
}
