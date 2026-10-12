package overlays

import (
	"os"
	"path/filepath"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/pulseaiclub/xui"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pulseaiclub/phi/internal/components"
	"github.com/pulseaiclub/phi/internal/components/status"
	"github.com/pulseaiclub/phi/internal/job"
	"github.com/pulseaiclub/phi/internal/llm"
	"github.com/pulseaiclub/phi/internal/session"
)

type snapshotStub struct {
	infos  []job.Info
	parent string
}

func (s *snapshotStub) Snapshot(parent string) []job.Info {
	s.parent = parent
	return append([]job.Info(nil), s.infos...)
}

func hubFrame(h *AgentHub, width int) components.Surface {
	return h.Draw(
		components.DrawContext{Max: components.Size{Width: width, Height: 8}, Method: xui.WidthUnicode},
		components.DefaultTheme(),
		"⠙",
	)
}

func hubKey(h *AgentHub, code xui.KeyCode) {
	h.Handle(
		&components.EventContext{},
		xui.KeyEvent{Press: true, Code: code},
		components.DefaultTheme(),
		&status.Spinner{},
	)
}

func TestHubAuthoritativeSnapshotAndRender(t *testing.T) {
	now := time.Now()
	source := &snapshotStub{
		infos: []job.Info{
			{
				Meta: job.Meta{
					ID:          "silent",
					Role:        job.RoleExplore,
					Status:      job.StatusRunning,
					CreatedAt:   now,
					Description: "你好🌍",
				},
			},
			{
				Meta: job.Meta{
					ID:         "done",
					Role:       job.RoleWorker,
					Status:     job.StatusCompleted,
					CreatedAt:  now.Add(time.Second),
					StartedAt:  now,
					FinishedAt: now.Add(3 * time.Second),
					Usage:      job.UsageDelta{CacheWrite: 42, CostUSD: .12},
				},
			},
		},
	}
	h := NewAgentHub(source)
	h.Refresh("parent")
	h.Toggle()
	assert.Equal(t, "parent", source.parent)
	require.Len(t, h.rows, 2)
	h.ApplyProgress(job.Progress{JobID: "done", Usage: &job.UsageDelta{CacheWrite: 999}})
	h.Refresh("parent")
	assert.Equal(t, 42, h.rows[1].Usage.CacheWrite)
	assert.Equal(t, 3*time.Second, h.rows[1].Elapsed)
	surf := hubFrame(h, 120)
	text := components.SurfaceText(surf)
	assert.Contains(t, text, "你好🌍")
	assert.Contains(t, text, "cache-write:42 $0.1200")
	assert.Contains(t, text, "1 running · 1 completed")
	assert.True(t, utf8.ValidString(components.SurfaceText(hubFrame(h, 19))))
	hubKey(h, xui.KeyDown)
	assert.Equal(t, 1, h.selected)
	hubKey(h, xui.KeyDown)
	assert.Equal(t, 1, h.selected)
	h.Refresh("other")
	assert.False(t, h.Active())
}

func TestHubChildReplayIsReadOnlyAndRefreshes(t *testing.T) {
	dir := t.TempDir()
	sm, err := session.NewSessionManager(
		dir,
		session.WithSessionDir(filepath.Join(dir, "session")),
		session.WithShouldFlush(true),
	)
	require.NoError(t, err)
	_, err = sm.Append(llm.Message{Role: llm.RoleUser, Content: "child question"})
	require.NoError(t, err)
	_, err = sm.Append(llm.Message{Role: llm.RoleAssistant, Content: "child answer"})
	require.NoError(t, err)
	before, err := os.ReadFile(sm.File())
	require.NoError(t, err)
	source := &snapshotStub{infos: []job.Info{{Meta: job.Meta{ID: "child", Dir: dir, Status: job.StatusRunning}}}}
	h := NewAgentHub(source)
	h.Refresh("parent")
	h.Toggle()
	hubKey(h, xui.KeyEnter)
	require.NotNil(t, h.child)
	require.Empty(t, h.loadErr)
	assert.Contains(t, components.SurfaceText(hubFrame(h, 100)), "child answer")
	after, err := os.ReadFile(sm.File())
	require.NoError(t, err)
	assert.Equal(t, before, after)
	_, err = sm.Append(llm.Message{Role: llm.RoleAssistant, Content: "new child answer"})
	require.NoError(t, err)
	h.lastRead = time.Time{}
	assert.Contains(t, components.SurfaceText(hubFrame(h, 100)), "new child answer")
	hubKey(h, xui.KeyEscape)
	assert.Empty(t, h.childID)
	assert.True(t, h.Active())
	hubKey(h, xui.KeyEscape)
	assert.False(t, h.Active())
}

func TestHubMissingTranscript(t *testing.T) {
	h := NewAgentHub(&snapshotStub{infos: []job.Info{{Meta: job.Meta{ID: "silent", Dir: t.TempDir()}}}})
	h.Refresh("parent")
	h.Toggle()
	hubKey(h, xui.KeyEnter)
	assert.Contains(t, components.SurfaceText(hubFrame(h, 100)), "Transcript not written yet")
}
