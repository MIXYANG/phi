package transcript_test

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pulseaiclub/phi/internal/components/status"
	"github.com/pulseaiclub/phi/internal/job"
	"github.com/pulseaiclub/phi/internal/tools"
	"github.com/pulseaiclub/phi/internal/tui/transcript"
)

func TestSubagentStoreIgnoresUsageAndLifecycle(t *testing.T) {
	s := transcript.NewSubagentStore()
	for _, p := range []job.Progress{
		{JobID: "job1", ParentToolUseID: "parent1", Usage: &job.UsageDelta{InputTokens: 12}},
		{JobID: "job1", ParentToolUseID: "parent1", Status: "completed"},
	} {
		require.False(t, s.ApplyProgress(p))
	}
	require.Empty(t, s.Children("parent1"))
}

func TestSubagentStoreParallelJobs(t *testing.T) {
	for _, toolUseID := range []string{"call_0", ""} {
		t.Run("tool_id="+toolUseID, func(t *testing.T) {
			s := transcript.NewSubagentStore()
			for _, jobID := range []string{"job2", "job1"} {
				s.Bind(jobID, "parent")
				require.True(t, s.ApplyProgress(job.Progress{
					JobID: jobID, ParentToolUseID: "parent", ToolUseID: toolUseID,
					Name: "read", Detail: "a.go", Status: "in-progress",
				}))
			}
			require.Len(t, s.Children("parent"), 2)
			require.Len(t, s.ChildrenByJob("job1"), 1)
			require.Len(t, s.ChildrenByJob("job2"), 1)

			update := job.Progress{
				JobID: "job1", ParentToolUseID: "parent", ToolUseID: toolUseID,
				Name: "read", Detail: "a.go", Status: "done",
			}
			require.True(t, s.ApplyProgress(update))
			require.False(t, s.ApplyProgress(update))
			s.Bind("job1", "parent")
			kids := s.Children("parent")
			require.Len(t, kids, 2)
			require.Equal(t, status.ToolRunning, kids[0].Status)
			require.Equal(t, status.ToolDone, kids[1].Status)
			require.Equal(t, kids[:1], s.ChildrenByJob("job2"))
			require.Equal(t, kids[1:], s.ChildrenByJob("job1"))
			kids[0].Detail = "changed"
			require.Equal(t, "a.go", s.Children("parent")[0].Detail)
			byJob := s.ChildrenByJob("job1")
			byJob[0].Detail = "changed"
			require.Equal(t, "a.go", s.ChildrenByJob("job1")[0].Detail)
		})
	}
}

func TestSubagentStoreProgressBeforeBind(t *testing.T) {
	s := transcript.NewSubagentStore()
	for _, jobID := range []string{"job1", "job2"} {
		require.True(t, s.ApplyProgress(job.Progress{
			JobID: jobID, ToolUseID: "call_0", Name: "read", Detail: jobID, Status: "in-progress",
		}))
	}
	s.Bind("job2", "spawn")
	s.Bind("job1", "spawn")
	s.Bind("job1", "wait")
	s.ApplyResult("wait", tools.ParseAgentResult(`{"job_id":"job1","status":"completed","summary":"ok"}`))
	require.True(t, s.ApplyProgress(job.Progress{
		JobID: "job1", ParentToolUseID: "spawn", ToolUseID: "call_0",
		Name: "read", Detail: "job1", Status: "done",
	}))
	kids := s.Children("spawn")
	require.Len(t, kids, 2)
	require.Equal(t, "job2", kids[0].Detail)
	require.Equal(t, status.ToolRunning, kids[0].Status)
	require.Equal(t, "job1", kids[1].Detail)
	require.Equal(t, status.ToolDone, kids[1].Status)
	require.Equal(t, kids[1:], s.Children("wait"))
	require.Equal(t, kids[1:], s.ChildrenByJob("job1"))
	require.Equal(t, kids[:1], s.ChildrenByJob("job2"))
}

func TestSubagentStoreParentOnlyProgress(t *testing.T) {
	s := transcript.NewSubagentStore()
	p := job.Progress{
		ParentToolUseID: "parent", ToolUseID: "call_0", Name: "read", Status: "in-progress",
	}
	require.True(t, s.ApplyProgress(p))
	s.Bind("job1", "parent")
	p.Status = "done"
	require.True(t, s.ApplyProgress(p))
	require.Len(t, s.Children("parent"), 1)
	require.Equal(t, status.ToolDone, s.Children("parent")[0].Status)
	require.Equal(t, s.Children("parent"), s.ChildrenByJob("job1"))
}

func TestSubagentStoreProgressAndResult(t *testing.T) {
	s := transcript.NewSubagentStore()
	s.Bind("job1", "parent1")
	s.ApplyProgress(job.Progress{
		JobID:           "job1",
		ParentToolUseID: "parent1",
		ToolUseID:       "c1",
		Name:            "read",
		Status:          "in-progress",
		Detail:          "a.go",
	})
	s.ApplyProgress(job.Progress{
		JobID:           "job1",
		ParentToolUseID: "parent1",
		ToolUseID:       "c1",
		Name:            "read",
		Status:          "done",
		Detail:          "a.go",
	})
	s.ApplyProgress(job.Progress{
		JobID:           "job1",
		ParentToolUseID: "parent1",
		ToolUseID:       "c2",
		Name:            "bash",
		Status:          "done",
		Detail:          "test",
	})

	kids := s.Children("parent1")
	require.Len(t, kids, 2)
	require.Equal(t, status.ToolDone, kids[0].Status)
	require.Equal(t, "read", kids[0].Name)
	byJob := s.ChildrenByJob("job1")
	require.Len(t, byJob, 2)

	s.ApplyResult("parent1", tools.ParseAgentResult(`{
		"job_id":"job1","status":"completed","summary":"## Ok"
	}`))
	// Summary is stored; Children unchanged.
	require.Len(t, s.Children("parent1"), 2, "children should not be cleared")
}
