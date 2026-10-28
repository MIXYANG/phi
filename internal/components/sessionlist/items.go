// Package sessionlist maps session.SessionMeta rows into listpicker items.
package sessionlist

import (
	"time"

	"github.com/pulseaiclub/phi/internal/components/chrome"
	"github.com/pulseaiclub/phi/internal/components/listpicker"
	"github.com/pulseaiclub/phi/internal/session"
)

// Config returns chrome copy for the sessions picker.
func Config() listpicker.ShowConfig {
	return listpicker.ShowConfig{
		Title:          "Sessions",
		FilterHint:     "filter title, id or preview…",
		Empty:          "No sessions in this directory",
		EmptyFilter:    "No sessions match %q",
		Hint:           chrome.ListHintExtra("resume", "ctrl+r rename", "ctrl+x delete"),
		ConfirmMessage: "Delete this session?",
		// Titles carry the word, so the primary column is wider than the
		// short-id column it replaces.
		PrimaryWidth: 28,
	}
}

// Items maps session metas into generic list rows. A named session leads with
// its title and keeps the short id in the detail column: resume, delete, and
// `phi sessions` all take the id, so it must stay visible.
func Items(list []session.SessionMeta, currentID string, now time.Time) []listpicker.Item {
	if now.IsZero() {
		now = time.Now()
	}
	out := make([]listpicker.Item, 0, len(list))
	for _, m := range list {
		detail := m.Preview
		if detail == "" {
			detail = "(no preview)"
		}
		primary := shortID(m.ID)
		if m.Title != "" {
			primary = m.Title
			detail = shortID(m.ID) + " · " + detail
		}
		item := listpicker.Item{
			ID:       m.ID,
			Leading:  FormatRelative(m.Mtime, now),
			Primary:  primary,
			Detail:   detail,
			Keywords: m.ID + " " + m.Title + " " + m.Preview,
			Edit:     m.Title,
		}
		if m.ID == currentID {
			item.Badge = "current"
		}
		out = append(out, item)
	}
	return out
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}
