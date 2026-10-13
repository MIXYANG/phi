package agent

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pulseaiclub/phi/internal/llm"
)

// TestSystemPromptFollowsSessionCwd pins PP-03: the engine builds its system
// prompt from the session cwd, so a sub-agent spawned with its own workdir sees
// that workspace's instructions instead of the parent process's.
func TestSystemPromptFollowsSessionCwd(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.Mkdir(filepath.Join(root, ".git"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("workdir-rule"), 0o644))
	work := filepath.Join(root, "pkg")
	require.NoError(t, os.Mkdir(work, 0o755))

	sess, err := NewSession(WithCwd(work))
	require.NoError(t, err)
	engine, err := NewEngine(llm.ModelConfig{Name: "fake"}, sess)
	require.NoError(t, err)

	got := engine.systemPrompt()
	require.Contains(t, got, "- Current directory: "+work)
	require.Contains(t, got, "- Workspace: "+root)
	require.Contains(t, got, "workdir-rule")

	processCwd, err := os.Getwd()
	require.NoError(t, err)
	require.NotContains(t, got, "- Current directory: "+processCwd)
	require.NotContains(t, got, "- Workspace: "+processCwd)
}
