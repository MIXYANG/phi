package agent

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/pulseaiclub/phi/internal/llm"
	"github.com/pulseaiclub/phi/internal/llm/anthropic"
	llmclient "github.com/pulseaiclub/phi/internal/llm/client"
)

// TestLiveAnthropicThinkingContinuation is the real-endpoint acceptance for
// C7a-anthropic; local fixtures cannot prove the API accepts the replayed
// signatures. It is opt-in because it spends real tokens:
//
//	PHI_LIVE_ANTHROPIC=1 ANTHROPIC_API_KEY=... \
//	  go test ./internal/agent/ -run TestLiveAnthropicThinkingContinuation -v
//
// PHI_LIVE_ANTHROPIC_MODEL overrides the model (default claude-sonnet-4-5).
// The tool is read-only and never executed; its result is appended by hand.
// Logs record the model and shapes only, never the key or native payloads.

// dropThinking clears the thinking parameter while leaving replayed history
// intact — the "no explicit thinking config" case an embedder's hook can hit.
type dropThinking struct{}

func (dropThinking) Before(
	_ context.Context, req *anthropic.AnthropicRequest, _ llm.ModelConfig,
) error {
	req.Thinking = nil
	return nil
}

func TestLiveAnthropicThinkingContinuation(t *testing.T) {
	if os.Getenv("PHI_LIVE_ANTHROPIC") != "1" {
		t.Skip("opt-in live acceptance: set PHI_LIVE_ANTHROPIC=1 and ANTHROPIC_API_KEY")
	}
	key := os.Getenv("ANTHROPIC_API_KEY")
	if key == "" {
		t.Fatal("PHI_LIVE_ANTHROPIC=1 requires ANTHROPIC_API_KEY")
	}
	model := os.Getenv("PHI_LIVE_ANTHROPIC_MODEL")
	if model == "" {
		model = "claude-sonnet-4-5"
	}
	cfg := llm.ModelConfig{
		API: llm.Anthropic, Name: model, APIKey: key,
		Think: llm.ThinkConfig{Enabled: true, Mode: llm.Medium},
	}
	defs := []llm.ToolDefinition{{
		Name:        "fixed_answer",
		Description: "Returns the fixed answer to any question.",
		Params:      &llm.FunctionParameters{Type: "object"},
	}}
	stream := func(client *llmclient.Client, msgs []llm.Message) *llm.Message {
		var final *llm.Message
		for ev, err := range client.Stream(t.Context(), msgs) {
			require.NoError(t, err)
			if ev.Final != nil {
				final = ev.Final
			}
		}
		require.NotNil(t, final)
		return final
	}
	answer := func(msg *llm.Message) llm.Message {
		require.NotEmpty(t, msg.ToolCalls)
		return llm.Message{Role: llm.RoleTool, ToolCallID: msg.ToolCalls[0].ID, Content: "42"}
	}

	dir := t.TempDir()
	sess, err := NewSession(WithCwd(dir), WithSessionDir(dir), WithPersist(true))
	require.NoError(t, err)
	require.NoError(t, sess.AddUser("Use the fixed_answer tool once, then stop."))
	client := llmclient.NewClient(cfg, llmclient.Hooks{}, defs, "")

	// Round 1 must really think: without reasoning there is nothing to verify.
	first := stream(client, sess.BuildContext())
	require.NotEmpty(t, first.ReasoningContent, "endpoint returned no thinking; nothing was verified")
	t.Logf("model=%s think=medium: round 1 reasoning=%d bytes tool_calls=%d",
		model, len(first.ReasoningContent), len(first.ToolCalls))
	require.NoError(t, sess.Append(*first))

	// Save, reload, and continue through the production session path.
	resumed, err := NewSession(WithResumePath(sess.File()))
	require.NoError(t, err)
	require.NoError(t, resumed.Append(answer(first)))
	second := stream(client, resumed.BuildContext())
	t.Logf("model=%s: round 2 continued after restore (reasoning=%d bytes)",
		model, len(second.ReasoningContent))
	require.NoError(t, resumed.Append(*second))
	for _, tc := range second.ToolCalls {
		require.NoError(t, resumed.Append(llm.Message{
			Role: llm.RoleTool, ToolCallID: tc.ID, Content: "42",
		}))
	}

	// A hook clearing the thinking parameter leaves "no explicit thinking
	// config", which replays the signed history unchanged. Only the real API
	// can confirm that shape is accepted; local fixtures cannot.
	cleared := llmclient.NewClient(cfg, llmclient.Hooks{Anthropic: dropThinking{}}, defs, "")
	require.NoError(t, resumed.AddUser("Continue with one more sentence."))
	_ = stream(cleared, resumed.BuildContext())
	t.Logf("model=%s: round 3 replayed signed history with the thinking parameter cleared", model)

	// A changed system prompt invalidates the prefix: stale thinking must be
	// filtered from the wire and the request must still be accepted.
	stranged := llmclient.NewClient(cfg, llmclient.Hooks{}, defs, "Answer in one word.")
	require.NoError(t, resumed.AddUser("Reply with one word."))
	_ = stream(stranged, resumed.BuildContext())
	t.Logf("model=%s: round 3 continued after the system prompt changed", model)
}
