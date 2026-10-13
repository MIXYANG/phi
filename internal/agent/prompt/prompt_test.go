package prompt

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBuildAgentsEnabledToggle(t *testing.T) {
	with := Build("", "", true, 4, nil)
	without := Build("", "", false, 0, nil)

	require.Contains(t, with, "agent_spawn")
	require.Contains(t, with, "Sub-agents:")
	require.Contains(t, with, "At most 4 sub-agents run concurrently")
	require.NotContains(t, without, "agent_spawn")
	require.NotContains(t, without, "sub-agents run concurrently")
	require.Contains(t, without, "`find` / `grep` / `ls` yourself")
}

func TestBuildEditHashCopyIsUnambiguous(t *testing.T) {
	got := Build("", "", false, 0, nil)
	require.Contains(t, got, "*** SM:EDIT path")
	require.Contains(t, got, "*** SM:FIND")
	require.NotContains(t, got, "Known path or exact symbol")
	require.NotContains(t, got, "creates a new file only")
	require.NotContains(t, got, "fails if it already exists")
	require.Contains(t, got, "`write` creates or overwrites")
	require.Contains(t, got, "Prefer cwd-relative paths")
}

func TestBuildMCPCatalog(t *testing.T) {
	none := Build("", "", false, 0, nil)
	require.NotContains(t, none, "# MCP")
	require.NotContains(t, none, "External docs/URLs")
	got := Build("", "", false, 0, []string{"browsermcp", "github"})
	require.Contains(t, got, "# MCP")
	require.Contains(t, got, "- browsermcp")
	require.Contains(t, got, "- github")
	require.Contains(t, got, "mcp_list")
	require.Contains(t, got, "mcp_inspect")
	require.Contains(t, got, "mcp_call")
	require.Contains(t, got, "docs/URLs")
	require.NotContains(t, got, `"properties"`)
	require.NotContains(t, got, "inputSchema")
}

// TestBuildUsesGivenCwd pins PP-03: the prompt describes the session's cwd and
// loads that workspace's instructions, not the process cwd's.
func TestBuildUsesGivenCwd(t *testing.T) {
	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, ".git"))
	mustWrite(t, filepath.Join(root, "AGENTS.md"), "workspace-local rule")
	nested := filepath.Join(root, "pkg", "deep")
	mustMkdir(t, nested)

	got := Build(nested, "", false, 0, nil)
	require.Contains(t, got, "- Current directory: "+nested)
	require.Contains(t, got, "- Workspace: "+root)
	require.Contains(t, got, "workspace-local rule")
}

func TestBuildIgnoresProcessCwdWhenGivenACwd(t *testing.T) {
	processCwd, err := os.Getwd()
	require.NoError(t, err)

	got := Build(t.TempDir(), "", false, 0, nil)
	require.NotContains(t, got, "- Current directory: "+processCwd)
	require.NotContains(t, got, "- Workspace: "+processCwd)
}

func TestBuildEmptyCwdFallsBackToProcessCwd(t *testing.T) {
	processCwd, err := os.Getwd()
	require.NoError(t, err)

	got := Build("", "", false, 0, nil)
	require.Contains(t, got, "- Current directory: "+processCwd)
}

// TestWorkspaceDir pins the git-root lookup used for the Workspace line.
func TestWorkspaceDir(t *testing.T) {
	require.Empty(t, workspaceDir(""), "empty dir has no workspace")

	root := t.TempDir()
	mustMkdir(t, filepath.Join(root, ".git"))
	nested := filepath.Join(root, "a", "b")
	mustMkdir(t, nested)

	require.Equal(t, root, workspaceDir(nested))
	require.Equal(t, root, workspaceDir(root))
}
