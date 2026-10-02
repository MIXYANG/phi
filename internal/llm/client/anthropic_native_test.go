package client

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pulseaiclub/phi/internal/llm"
	"github.com/pulseaiclub/phi/internal/llm/anthropic"
)

type anthropicFixedModel string

func (model anthropicFixedModel) Before(_ context.Context, req *anthropic.AnthropicRequest, _ llm.ModelConfig) error {
	req.Model = string(model)
	return nil
}

func TestClientAnthropicRejectsFixedModelRewrite(t *testing.T) {
	const sse = `data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"","signature":"signed-by-b"}}

data: {"type":"content_block_stop","index":0}

data: {"type":"message_stop"}

`
	var requests atomic.Int32
	bodies := make(chan json.RawMessage, 4)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		var body json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		bodies <- body
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(w, sse)
	}))
	defer srv.Close()
	cfg := llm.ModelConfig{
		API:     llm.Anthropic,
		Name:    "alias-a",
		BaseURL: srv.URL,
		Think:   llm.ThinkConfig{Enabled: true, Mode: llm.Medium},
	}
	hooks := Hooks{Anthropic: anthropicFixedModel("model-b")}
	alias := NewClient(cfg, hooks, nil, "")
	messages := []llm.Message{{Role: llm.RoleUser, Content: "think"}}
	events := collectEvents(alias.Stream(t.Context(), messages))
	require.Len(t, events, 1)
	assert.Equal(t, llm.StreamEventTypeError, events[0].Type)
	assert.Contains(t, events[0].Err, "request hook changed model")
	assert.Zero(t, requests.Load(), "reject the rewrite before the first HTTP request")

	// Configuring the actual model lets the same fixed hook retain signatures.
	cfg.Name = "model-b"
	actual := NewClient(cfg, hooks, nil, "")
	events = collectEvents(actual.Stream(t.Context(), messages))
	require.Len(t, events, 1)
	require.NotNil(t, events[0].Final)
	require.NotNil(t, events[0].Final.Native)
	assert.Equal(t, "model-b", events[0].Final.Native.Model)
	messages = append(messages, *events[0].Final, llm.Message{Role: llm.RoleUser, Content: "continue"})
	<-bodies
	events = collectEvents(actual.Stream(t.Context(), messages))
	require.Len(t, events, 1)
	require.NotNil(t, events[0].Final)
	assert.Contains(t, string(<-bodies), `"signature":"signed-by-b"`)
	count := requests.Load()

	// Existing model-b history must not disappear behind the alias either.
	events = collectEvents(alias.Stream(t.Context(), messages))
	require.Len(t, events, 1)
	assert.Equal(t, llm.StreamEventTypeError, events[0].Type)
	assert.Contains(t, events[0].Err, "request hook changed model")
	assert.Equal(t, count, requests.Load())
}
