package compaction

import (
	"slices"

	"github.com/pulseaiclub/phi/internal/llm"
	"github.com/pulseaiclub/phi/internal/session"
)

// Settings configures compaction: whether it is enabled and the token
// thresholds used to decide when to compact.
type Settings struct {
	enabled          bool
	reverseTokens    int
	keepRecentTokens int
}

var defaultSettings = Settings{
	enabled:          true,
	reverseTokens:    16384,
	keepRecentTokens: 20000,
}

// DefaultSettings returns the default compaction settings for use by callers outside this package.
func DefaultSettings() Settings {
	return defaultSettings
}

// ForWindow fits keepRecentTokens to contextWindow. The kept tail must sit well
// under the trigger (window - reserve): a tail at or over it leaves the next
// request still past the threshold, so every later tool round pays for another
// summary. Half the trigger leaves room for the summary and a few more rounds.
// An unknown window (<= 0) keeps the defaults.
func (s Settings) ForWindow(contextWindow int) Settings {
	if contextWindow <= 0 {
		return s
	}
	threshold := max(contextWindow-s.reverseTokens, 0)
	s.keepRecentTokens = min(s.keepRecentTokens, threshold/2)
	return s
}

// EstimateContextTokens estimates the size of the request the entries would
// produce: the last provider-reported context size plus a chars/4 estimate of
// everything appended after it. Only the reported size covers the system prompt
// and the tool schemas, so it anchors the estimate, and the messages after it
// are what a mid-turn check has to add. Without any reported usage the whole
// path is estimated.
//
// Non-message entries are skipped: the newest compaction entry precedes every
// kept message, so its summary is already part of an anchor's reported size.
func EstimateContextTokens(entries []session.MessageEntry) int {
	anchor := -1
	for i := range slices.Backward(entries) {
		if entries[i].GetType() != session.EntryMessage {
			continue
		}
		msgEntry := entries[i].(session.SessionMessageEntry)
		if msgEntry.Message.Role == llm.RoleAssistant && msgEntry.Usage.ContextTokens() > 0 {
			anchor = i
			break
		}
	}

	total := 0
	start := 0
	if anchor >= 0 {
		total = entries[anchor].(session.SessionMessageEntry).Usage.ContextTokens()
		start = anchor + 1
	}
	for _, entry := range entries[start:] {
		if entry.GetType() != session.EntryMessage {
			continue
		}
		total += estimateMessageTokens(entry.(session.SessionMessageEntry).Message)
	}
	return total
}

// ShouldCompact reports whether contextTokens warrants compaction given
// contextWindow and settings.
func ShouldCompact(contextTokens, contextWindow int, settings Settings) bool {
	if !settings.enabled || contextWindow <= 0 {
		return false
	}

	// Keep `reverseTokens` headroom from the context window. When current usage
	// exceeds (contextWindow - reverseTokens), we should compact.
	threshold := max(contextWindow-settings.reverseTokens, 0)
	return contextTokens > threshold
}
