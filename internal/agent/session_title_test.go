package agent

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pulseaiclub/phi/internal/llm"
	"github.com/pulseaiclub/phi/internal/session"
)

// persistedSession opens a persisted session in dir, the way the TUI does.
func persistedSession(t *testing.T, dir string) *Session {
	t.Helper()
	sess, err := NewSession(WithSessionDir(dir), WithPersist(true), WithCwd(dir))
	require.NoError(t, err)
	return sess
}

func TestFirstPromptNamesSession(t *testing.T) {
	dir := t.TempDir()
	sess := persistedSession(t, dir)

	require.NoError(t, sess.Append(llm.Message{
		Role:    llm.RoleUser,
		Content: "why does the resume path leak goroutines?",
	}))
	assert.Equal(t, "why does the resume path leak goroutines?", sess.Title())

	// The name lands on disk with the first flush, not before it.
	require.NoError(t, sess.Append(llm.Message{Role: llm.RoleAssistant, Content: "it does not cancel"}))

	list, err := session.ListSessions(dir)
	require.NoError(t, err)
	require.Len(t, list, 1)
	assert.Equal(t, "why does the resume path leak goroutines?", list[0].Title)
}

func TestFirstPromptNameIsNormalized(t *testing.T) {
	sess := persistedSession(t, t.TempDir())

	require.NoError(t, sess.Append(llm.Message{
		Role:    llm.RoleUser,
		Content: "  fix\n\tthe   resume   leak  ",
	}))
	assert.Equal(t, "fix the resume leak", sess.Title())
}

// A later prompt must not rename the session, and an explicit name wins over
// the prompt-derived one.
func TestOnlyTheFirstPromptNamesSession(t *testing.T) {
	sess := persistedSession(t, t.TempDir())

	require.NoError(t, sess.Append(llm.Message{Role: llm.RoleUser, Content: "first prompt"}))
	require.NoError(t, sess.Append(llm.Message{Role: llm.RoleAssistant, Content: "ok"}))
	require.NoError(t, sess.Append(llm.Message{Role: llm.RoleUser, Content: "second prompt"}))
	assert.Equal(t, "first prompt", sess.Title())

	require.NoError(t, sess.SetTitle("chosen by hand"))
	require.NoError(t, sess.Append(llm.Message{Role: llm.RoleUser, Content: "third prompt"}))
	assert.Equal(t, "chosen by hand", sess.Title())
}

// A conversation is named by what the user asked for, so a non-user opening
// message leaves the session unnamed instead of borrowing its text.
func TestNonUserMessagesDoNotNameSession(t *testing.T) {
	sess := persistedSession(t, t.TempDir())

	require.NoError(t, sess.Append(llm.Message{Role: llm.RoleAssistant, Content: "hello"}))
	assert.Empty(t, sess.Title())
}

func TestEmptyPromptLeavesSessionUnnamed(t *testing.T) {
	sess := persistedSession(t, t.TempDir())

	require.NoError(t, sess.Append(llm.Message{Role: llm.RoleUser, Content: "  \n\t "}))
	assert.Empty(t, sess.Title())
}

func TestLongFirstPromptIsCapped(t *testing.T) {
	sess := persistedSession(t, t.TempDir())

	require.NoError(t, sess.Append(llm.Message{Role: llm.RoleUser, Content: strings.Repeat("word ", 60)}))
	title := sess.Title()
	assert.LessOrEqual(t, len([]rune(title)), session.MaxTitleRunes+1) // + ellipsis
	assert.True(t, strings.HasSuffix(title, "…"))
}
