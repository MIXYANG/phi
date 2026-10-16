package editor

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pulseaiclub/xui"

	"github.com/pulseaiclub/phi/internal/components"
	"github.com/pulseaiclub/phi/internal/components/app"
	"github.com/pulseaiclub/phi/internal/components/chrome"
	"github.com/pulseaiclub/phi/internal/components/palette"
	"github.com/pulseaiclub/phi/internal/extension/loader"
	"github.com/pulseaiclub/phi/internal/llm"
	"github.com/pulseaiclub/phi/internal/project"
	"github.com/pulseaiclub/phi/internal/tui/commands"
	"github.com/pulseaiclub/phi/internal/tui/controller"
)

func TestPaletteReopenReflectsThinkingSelectionAndModelSwitch(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	for _, key := range []string{"PHI_MODEL", "PHI_API_KEY", "PHI_BASE_URL", "PHI_THINK_LEVEL"} {
		t.Setenv(key, "")
	}
	t.Setenv(loader.EnvExtensions, "off")
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".phi"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(home, ".phi", "config.yaml"), []byte(`models:
  - name: primary
    default: true
    api: OpenAI
    api_key: test-key
    base_url: http://127.0.0.1:9
    think_level: off
  - name: secondary
    api: OpenAI
    api_key: test-key
    base_url: http://127.0.0.1:9
    think_level: high
`), 0o600))
	cwd := t.TempDir()
	proj, err := project.Discover(cwd)
	require.NoError(t, err)
	bus := controller.NewBus(nil)
	ctrl, err := controller.NewController(bus, proj, cwd)
	require.NoError(t, err)
	t.Cleanup(ctrl.Close)
	e := NewEditor(
		app.NewApp(nil),
		bus,
		ctrl,
		nil,
		components.DefaultTheme(),
		cwd,
		"primary",
		nil,
		0,
		[]string{"primary", "secondary"},
	)

	key := func(code xui.KeyCode) {
		e.composer.Handle(&components.EventContext{}, xui.KeyEvent{Press: true, Code: code})
	}
	openSettings := func(query, title string) *palette.CommandPalette {
		t.Helper()
		e.composer.Handle(
			&components.EventContext{},
			xui.KeyEvent{Press: true, Code: xui.KeyRune, Rune: 'k', Mods: xui.ModCtrl},
		)
		for _, r := range query {
			e.composer.Handle(&components.EventContext{}, xui.KeyEvent{Press: true, Code: xui.KeyRune, Rune: r})
		}
		key(xui.KeyEnter)
		overlay, ok := e.composer.PaletteOverlay(
			components.DrawContext{Max: components.Size{Width: 100, Height: 30}, Method: xui.WidthUnicode},
		)
		require.True(t, ok)
		p, ok := overlay.Surface.Widget.(*palette.CommandPalette)
		require.True(t, ok)
		require.Equal(t, title, p.Title)
		return p
	}
	assertCurrent := func(p *palette.CommandPalette, current llm.ThinkMode) {
		t.Helper()
		for _, cmd := range p.Commands {
			level := cmd.ID[len("think-"):]
			assert.Equal(t, chrome.CurrentMarker(level == string(current))+level, cmd.Verb)
		}
	}

	p := openSettings("think", "Thinking Level")
	assertCurrent(p, llm.Off)
	key(xui.KeyDown) // minimal
	key(xui.KeyDown) // low
	key(xui.KeyEnter)
	require.False(t, p.Open)
	require.Equal(t, llm.Low, ctrl.ThinkLevel())
	p = openSettings("think", "Thinking Level")
	assertCurrent(p, llm.Low)
	e.composer.HidePalette()

	p = openSettings("settings model", "Select Model")
	require.Len(t, p.Commands, 2)
	assert.Equal(t, chrome.CurrentMarker(true)+"primary", p.Commands[0].Verb)
	assert.Equal(t, chrome.CurrentMarker(false)+"secondary", p.Commands[1].Verb)
	key(xui.KeyDown)
	key(xui.KeyEnter)
	require.False(t, p.Open)
	require.Equal(t, "secondary", ctrl.ModelName())

	p = openSettings("settings model", "Select Model")
	require.Len(t, p.Commands, 2)
	assert.Equal(t, chrome.CurrentMarker(false)+"primary", p.Commands[0].Verb)
	assert.Equal(t, chrome.CurrentMarker(true)+"secondary", p.Commands[1].Verb)
	e.composer.HidePalette()

	p = openSettings("think", "Thinking Level")
	assertCurrent(p, llm.High)
}

func TestPaletteRefreshesRegistryOnlyWhenOpening(t *testing.T) {
	e := newTestEditor(t)
	current := "first"
	builds := 0
	e.commands.Register(commands.Command{
		Name: "live-setting",
		Build: func(commands.Context) palette.PaletteCommand {
			builds++
			return palette.PaletteCommand{ID: "live-setting", Verb: current}
		},
	})
	rec := &recordingComposer{}
	e.extCmds.Composer = rec
	toggle := func() {
		e.composer.Handle(
			&components.EventContext{},
			xui.KeyEvent{Press: true, Code: xui.KeyRune, Rune: 'k', Mods: xui.ModCtrl},
		)
	}
	toggle()
	require.Equal(t, 1, builds)
	assert.Equal(t, "first", rec.last[len(rec.last)-1].Verb)
	toggle()
	assert.Equal(t, 1, builds, "closing must not rebuild commands before Hide restores parent frames")
	current = "second"
	toggle()
	require.Equal(t, 2, builds)
	assert.Equal(t, "second", rec.last[len(rec.last)-1].Verb)
}
