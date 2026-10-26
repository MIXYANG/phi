package compaction

import (
	"context"
	_ "embed"
	"fmt"
	"strings"

	"github.com/pulseaiclub/phi/internal/llm"
)

var (
	//go:embed compaction-summary.tmpl
	compactionSummaryPrompt string
	//go:embed compaction-update-summary.tmpl
	compactionUpdateSummaryPrompt string
	//go:embed compaction-turn-prefix.tmpl
	compactionTurnPrefixPrompt string
)

// Summary output caps are fixed instead of derived from reserveTokens. The
// summary request is far smaller than the live context (kept messages, the
// system prompt and tools are not sent), so the reserved headroom says nothing
// about how much output it can afford. Reasoning shares this cap on most
// providers and varies widely from call to call, and each update must carry
// the previous summary forward, so a cap tied to the reserve (0.8 × 16384)
// eventually truncated every compaction of a long session. The values sit
// under common model output limits but well above what a summary body needs.
// They are sent explicitly: omitting the cap falls back to provider defaults
// that are lower still (Anthropic requires one; DeepSeek non-thinking stops at
// 8192). The turn-prefix cap is smaller because it only recovers the tail of
// one turn.
const (
	historySummaryMaxTokens    = 32768
	turnPrefixSummaryMaxTokens = 16384
)

// errTruncatedSummary reports a summary that hit the output cap: its text is a
// prefix of the summary the model meant to write, so persisting it would drop
// the history it replaced.
func errTruncatedSummary(label string) error {
	return fmt.Errorf("%s failed: generation hit the token cap, summary incomplete", label)
}

// errEmptySummary reports a finished generation that carries no text. A blank
// summary persists nothing on its own, and the file list appended around it
// would end up as a checkpoint that lost the history it replaced.
func errEmptySummary(label string) error {
	return fmt.Errorf("%s failed: model returned an empty summary", label)
}

// generateSummary generates a summary of the conversation history using an LLM.
// If a previousSummary is provided, it will update the existing summary with new messages.
// Otherwise, it generates an initial summary from the currentMessages.
func generateSummary(
	ctx context.Context,
	compactor llm.Compactor,
	currentMessages []llm.Message,
	previousSummary string,
	maxTokens int,
) (string, error) {
	basePrompt := compactionSummaryPrompt
	if previousSummary != "" {
		basePrompt = compactionUpdateSummaryPrompt
	}

	conversation := SerializeConversation(currentMessages)
	promptText := fmt.Sprintf("<conversation>\n%s\n</conversation>", conversation)
	if previousSummary != "" {
		promptText += fmt.Sprintf("<previous-summary>\n%s\n</previous-summary>", previousSummary)
	}
	promptText += basePrompt
	return compact(ctx, compactor, promptText, maxTokens, "Summarization")
}

// generateTurnPrefixSummary generates a summary specifically for turn prefixes
// (prefix-based compaction): the prefix of a turn whose suffix is kept. Without
// the prompt the model is handed a bare conversation and no instruction, and its
// continuation gets persisted as the session summary.
func generateTurnPrefixSummary(
	ctx context.Context,
	compactor llm.Compactor,
	messages []llm.Message,
	maxTokens int,
) (string, error) {
	conversation := SerializeConversation(messages)
	promptText := fmt.Sprintf("<conversation>\n%s\n</conversation>\n\n", conversation)
	promptText += compactionTurnPrefixPrompt
	return compact(ctx, compactor, promptText, maxTokens, "Turn prefix summarization")
}

// compact runs one summarization call and rejects a capped or blank answer:
// either way there is no usable summary, and persisting what came back would
// silently drop the history it is meant to stand in for.
func compact(
	ctx context.Context,
	compactor llm.Compactor,
	prompt string,
	maxTokens int,
	label string,
) (string, error) {
	res, err := compactor.Compact(ctx, llm.CompactRequest{Prompt: prompt, MaxTokens: maxTokens})
	if err != nil {
		return "", err
	}
	if res.Truncated {
		return "", errTruncatedSummary(label)
	}
	if strings.TrimSpace(res.Text) == "" {
		return "", errEmptySummary(label)
	}
	return res.Text, nil
}
