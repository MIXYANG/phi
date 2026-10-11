package core

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pulseaiclub/phi/internal/extension/loader"
)

func TestFiberLoadFailureAndDispose(t *testing.T) {
	discovered := loader.Discovered{
		Manifest: loader.Manifest{Name: "broken", Exec: "/nonexistent/phi-test-ext"},
		Path:     t.TempDir(),
	}
	f := NewFiber("broken", discovered)
	require.Equal(t, FiberPending, f.State())

	err := f.Load(t.Context(), StartEnv{LogDir: t.TempDir()})
	require.Error(t, err)
	assert.Equal(t, FiberFailed, f.State())
	assert.Error(t, f.Error())

	require.NoError(t, f.Unload())
	assert.Equal(t, FiberDisposed, f.State())

	// Unloading twice is a no-op, and a disposed fiber cannot come back.
	require.NoError(t, f.Unload())
	assert.Equal(t, FiberDisposed, f.State())
	assert.Error(t, f.Load(t.Context(), StartEnv{LogDir: t.TempDir()}))
}

func TestFiberStateMachineGuards(t *testing.T) {
	f := NewFiber("idle", loader.Discovered{Manifest: loader.Manifest{Name: "idle"}})

	// A disposed fiber refuses reloads instead of starting a second process.
	require.NoError(t, f.Unload())
	assert.Equal(t, FiberDisposed, f.State())
	assert.ErrorContains(t, f.Reload(t.Context(), StartEnv{}), "disposed")
}

func TestFiberStateStrings(t *testing.T) {
	assert.Equal(t, "pending", FiberPending.String())
	assert.Equal(t, "loading", FiberLoading.String())
	assert.Equal(t, "active", FiberActive.String())
	assert.Equal(t, "failed", FiberFailed.String())
	assert.Equal(t, "disposed", FiberDisposed.String())
	assert.Equal(t, "unknown", FiberState(99).String())
}
