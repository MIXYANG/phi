package editor

import (
	"testing"

	"github.com/pulseaiclub/xui"
	"github.com/stretchr/testify/assert"

	"github.com/pulseaiclub/phi/internal/components"
	"github.com/pulseaiclub/phi/internal/job"
	"github.com/pulseaiclub/phi/internal/tui/controller"
	"github.com/pulseaiclub/phi/internal/tui/overlays"
)

func TestHubShortcutFromComposerAndOverlayFocus(t *testing.T) {
	e := newTestEditor(t)
	key := xui.KeyEvent{Press: true, Code: xui.KeyRune, Rune: 'a', Mods: xui.ModAlt}
	e.composer.Handle(&components.EventContext{}, key)
	assert.True(t, e.agentHub.Active())
	assert.True(t, e.blocksComposer())
	assert.Contains(t, components.SurfaceText(frame(t, e)), "Agent Hub")
	assert.Contains(t, components.SurfaceText(frame(t, e)), "No sub-agents")
	e.Handle(&components.EventContext{}, xui.KeyEvent{Press: true, Code: xui.KeyEscape})
	assert.False(t, e.agentHub.Active())
	assert.False(t, e.blocksComposer())
	e.openCode([]string{"a.go"})
	e.Handle(&components.EventContext{}, key)
	assert.True(t, e.agentHub.Active())
	assert.False(t, e.code.Active())
	e.Update(controller.OverlayMsg{Kind: controller.OverlayContinueAsk, MaxRounds: 3})
	assert.Contains(t, components.SurfaceText(frame(t, e)), "Reached max tool rounds")
	assert.False(t, e.agentHub.Active())
	e.Handle(&components.EventContext{}, key)
	assert.False(t, e.agentHub.Active())
}

type hubJobs struct{}

func (hubJobs) Snapshot(string) []job.Info {
	return []job.Info{{Meta: job.Meta{ID: "live", Role: job.RoleReview, Status: job.StatusStarting}}}
}

func TestPinnedHubBarPaintedAboveComposer(t *testing.T) {
	e := newTestEditor(t)
	e.agentHub = overlays.NewAgentHub(hubJobs{})
	e.agentHub.Refresh("parent")
	surf := frame(t, e)
	assert.Contains(t, components.SurfaceText(surf), "review starting")
	assert.Equal(t, 1, e.pinnedBarHeight())
	var barY, chatY int
	for _, child := range surf.Children {
		if child.Surface.Size.Height == 1 && child.Origin.Y != 29 {
			barY = child.Origin.Y
		}
		if child.Z == 1 {
			chatY = child.Origin.Y
		}
	}
	assert.Equal(t, barY+1, chatY)
}
