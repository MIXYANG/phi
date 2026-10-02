package anthropic

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pulseaiclub/phi/internal/llm"
)

func thinkingStream(events ...string) string {
	return "data: " + strings.Join(events, "\n\ndata: ") + "\n\n"
}

func TestNativeThinkingCapture(t *testing.T) {
	cfg := llm.ModelConfig{Name: "claude-test", BaseURL: "https://provider.test", Think: llm.ThinkConfig{Enabled: true}}
	for _, tt := range []struct {
		name      string
		block     string
		deltas    []string
		want      string
		reasoning string
	}{
		{
			name:  "signed text",
			block: `{"type":"thinking","thinking":" start\n","signature":"prefix/"}`,
			deltas: []string{
				`{"type":"thinking_delta","thinking":"第一步\n"}`,
				`{"type":"signature_delta","signature":"opaque+"}`,
				`{"type":"signature_delta","signature":"=="}`,
			},
			want:      `{"type":"thinking","thinking":" start\n第一步\n","signature":"prefix/opaque+=="}`,
			reasoning: " start\n第一步\n",
		},
		{
			name:   "omitted text",
			block:  `{"type":"thinking","thinking":""}`,
			deltas: []string{`{"type":"signature_delta","signature":"opaque"}`},
			want:   `{"type":"thinking","thinking":"","signature":"opaque"}`,
		},
		{
			name:  "redacted only",
			block: `{"type":"redacted_thinking","data":"encrypted/+=="}`,
			want:  `{"type":"redacted_thinking","data":"encrypted/+=="}`,
		},
		{
			name:      "unsigned compatible endpoint",
			block:     `{"type":"thinking","thinking":"display only"}`,
			reasoning: "display only",
		},
		{
			name:  "missing redacted data",
			block: `{"type":"redacted_thinking"}`,
		},
		{
			name:  "plain text",
			block: `{"type":"text","text":"hello"}`,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			events := []string{`{"type":"content_block_start","index":0,"content_block":` + tt.block + `}`}
			for _, delta := range tt.deltas {
				events = append(events, `{"type":"content_block_delta","index":0,"delta":`+delta+`}`)
			}
			events = append(events, `{"type":"content_block_stop","index":0}`, `{"type":"message_stop"}`)
			var final *llm.Message
			var display strings.Builder
			processStream(strings.NewReader(thinkingStream(events...)), cfg, func(ev llm.StreamEvent, err error) bool {
				require.NoError(t, err)
				display.WriteString(ev.Delta.ReasoningContent)
				if ev.Final != nil {
					final = ev.Final
				}
				return true
			})
			require.NotNil(t, final)
			assert.Equal(t, tt.reasoning, final.ReasoningContent)
			assert.Equal(t, tt.reasoning, display.String())
			if tt.want == "" {
				assert.Nil(t, final.Native)
				return
			}
			require.NotNil(t, final.Native)
			assert.Equal(t, nativeStateVersion, final.Native.Version)
			assert.Equal(t, llm.Anthropic, final.Native.API)
			assert.Equal(t, cfg.Name, final.Native.Model)
			assert.Equal(t, endpointFingerprint(cfg.BaseURL), final.Native.Endpoint)
			require.Len(t, final.Native.Items, 1)
			assert.JSONEq(t, tt.want, string(final.Native.Items[0]))
			req := BuildRequest(cfg, "", []llm.Message{*final}, nil)
			require.Len(t, req.Messages, 1)
			body, err := json.Marshal(req.Messages[0].Content)
			require.NoError(t, err)
			assert.JSONEq(t, "["+tt.want+"]", string(body))
		})
	}
}

func TestNativeThinkingRejectsIncompleteStream(t *testing.T) {
	start := `{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`
	signature := `{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"opaque"}}`
	stop := `{"type":"content_block_stop","index":0}`
	for _, tt := range []struct {
		name   string
		events []string
		want   string
	}{
		{"unfinished block", []string{start, signature}, "incomplete thinking content"},
		{"missing message stop", []string{start, signature, stop}, "incomplete thinking content"},
		{"malformed delta", []string{start, `{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":42}}`, signature, stop, `{"type":"message_stop"}`}, "incomplete thinking content"},
		{"malformed json", []string{start, `{"type":`, signature, stop, `{"type":"message_stop"}`}, "incomplete thinking content"},
		{"message stop before block stop", []string{start, signature, `{"type":"message_stop"}`}, "incomplete thinking content"},
		{"late block stop", []string{start, signature, `{"type":"message_stop"}`, stop}, "incomplete thinking content"},
		{"thinking after message stop", []string{`{"type":"message_stop"}`, start, signature, stop}, "after message_stop"},
		{"duplicate message stop", []string{start, signature, stop, `{"type":"message_stop"}`, `{"type":"message_stop"}`}, "after message_stop"},
		{"signature after message stop", []string{start, signature, stop, `{"type":"message_stop"}`, signature}, "after message_stop"},
		{"orphan thinking with tool", []string{
			`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"orphan"}}`,
			`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"read","input":{}}}`,
			stop, `{"type":"message_stop"}`,
		}, "incomplete thinking content"},
		{"orphan signature with tool", []string{
			signature,
			`{"type":"content_block_start","index":0,"content_block":{"type":"tool_use","id":"toolu_1","name":"read","input":{}}}`,
			stop, `{"type":"message_stop"}`,
		}, "incomplete thinking content"},
		{"wrong block index", []string{start, `{"type":"content_block_delta","index":1,"delta":{"type":"signature_delta","signature":"opaque"}}`, stop, `{"type":"message_stop"}`}, "incomplete thinking content"},
		{"provider error", []string{start, signature, stop, `{"type":"error","error":{"type":"overloaded_error","message":"busy"}}`}, "busy"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var streamErr error
			processStream(
				strings.NewReader(thinkingStream(tt.events...)),
				llm.ModelConfig{},
				func(ev llm.StreamEvent, err error) bool {
					assert.Nil(t, ev.Final)
					if err != nil {
						streamErr = err
					}
					return true
				},
			)
			require.ErrorContains(t, streamErr, tt.want)
		})
	}
}

func TestNativeToolResultIDsMatch(t *testing.T) {
	cfg := llm.ModelConfig{
		Name:    "claude-test",
		BaseURL: "https://provider.test",
		API:     llm.Anthropic,
		Think:   llm.ThinkConfig{Enabled: true},
	}
	for _, id := range []string{"toolu_normal", "call|with.special/chars", strings.Repeat("a", 65)} {
		t.Run(id, func(t *testing.T) {
			tool, err := json.Marshal(map[string]any{
				"type": "tool_use", "id": id, "name": "read", "input": map[string]any{},
			})
			require.NoError(t, err)
			msg := llm.Message{
				Role: llm.RoleAssistant,
				ToolCalls: []llm.ToolCall{
					{ID: id, Type: "function", Function: llm.Function{Name: "read", Arguments: "{}"}},
				},
				Native: &llm.NativeState{
					Version:  nativeStateVersion,
					API:      llm.Anthropic,
					Model:    cfg.Name,
					Endpoint: endpointFingerprint(cfg.BaseURL),
					Items: []json.RawMessage{
						json.RawMessage(`{"type":"thinking","thinking":"","signature":"opaque"}`),
						tool,
					},
				},
			}
			messages := []llm.Message{msg, {Role: llm.RoleTool, ToolCallID: id, Content: "ok"}}
			req := BuildRequest(cfg, "", messages, nil)
			require.Len(t, req.Messages, 2)
			assert.Equal(t, msg.Native.Items, req.Messages[0].Content, "native blocks must remain intact")
			results := req.Messages[1].Content.([]anthropicContentBlock)
			require.Len(t, results, 1)
			assert.Equal(t, id, results[0].ToolUseID)

			// Reusing an ID in a later legacy turn must not inherit native routing.
			legacy := msg
			legacy.Native = nil
			mixed := append([]llm.Message{msg, messages[1], legacy, messages[1]}, msg, messages[1])
			req = BuildRequest(cfg, "", mixed, nil)
			require.Len(t, req.Messages, 6)
			assert.Equal(t, id, req.Messages[1].Content.([]anthropicContentBlock)[0].ToolUseID)
			assert.Equal(t, normalizeToolCallID(id), req.Messages[3].Content.([]anthropicContentBlock)[0].ToolUseID)
			assert.Equal(t, id, req.Messages[5].Content.([]anthropicContentBlock)[0].ToolUseID)

			// A source switch uses the ordinary projection on both sides.
			other := cfg
			other.Name = "other-model"
			req = BuildRequest(other, "", messages, nil)
			calls := req.Messages[0].Content.([]anthropicContentBlock)
			results = req.Messages[1].Content.([]anthropicContentBlock)
			assert.Equal(t, normalizeToolCallID(id), calls[0].ID)
			assert.Equal(t, calls[0].ID, results[0].ToolUseID)
		})
	}
}

func TestNativeThinkingSourceIsolation(t *testing.T) {
	cfg := llm.ModelConfig{
		Name:    "claude-test",
		BaseURL: "https://provider.test",
		API:     llm.Anthropic,
		Think:   llm.ThinkConfig{Enabled: true},
	}
	state := llm.NativeState{
		Version: nativeStateVersion, API: llm.Anthropic, Model: cfg.Name, Endpoint: endpointFingerprint(cfg.BaseURL),
		Items: []json.RawMessage{json.RawMessage(`{"type":"thinking","thinking":"","signature":"opaque"}`)},
	}
	for _, tt := range []struct {
		name   string
		change func(*llm.NativeState)
	}{
		{"model", func(s *llm.NativeState) { s.Model = "other-model" }},
		{"api", func(s *llm.NativeState) { s.API = llm.OpenAIResponses }},
		{"endpoint", func(s *llm.NativeState) { s.Endpoint = endpointFingerprint("https://other.test") }},
		{"unknown version", func(s *llm.NativeState) { s.Version++ }},
		{"missing endpoint", func(s *llm.NativeState) { s.Endpoint = "" }},
	} {
		t.Run(tt.name, func(t *testing.T) {
			other := state
			tt.change(&other)
			msg := llm.Message{Role: llm.RoleAssistant, Content: "visible", Native: &other}
			req := BuildRequest(cfg, "", []llm.Message{msg}, nil)
			require.Len(t, req.Messages, 1)
			assert.Equal(t, "visible", req.Messages[0].Content)
			msg.Content = ""
			req = BuildRequest(cfg, "", []llm.Message{msg, {Role: llm.RoleUser, Content: "continue"}}, nil)
			require.Len(t, req.Messages, 1)
			assert.Equal(t, "user", req.Messages[0].Role)
		})
	}
	assert.Equal(t, endpointFingerprint(""), endpointFingerprint(defaultBaseURL+"/"))
	assert.Equal(t, state.Endpoint, endpointFingerprint(cfg.BaseURL+"/v1/"))
	assert.NotContains(t, endpointFingerprint("https://user:secret@provider.test/?key=secret"), "secret")
	msg := llm.Message{Role: llm.RoleAssistant, Native: &state}
	req := BuildRequest(cfg, "", []llm.Message{msg}, nil)
	items := req.Messages[0].Content.([]json.RawMessage)
	items[0][0] = '['
	assert.Equal(t, byte('{'), state.Items[0][0], "request mutation must not touch session bytes")
	again := BuildRequest(cfg, "", []llm.Message{msg}, nil)
	assert.Equal(t, state.Items, again.Messages[0].Content)

	again.Model = "hook-selected-model"
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	for _, err := range Stream(ctx, &http.Client{}, cfg, &again) {
		require.ErrorContains(t, err, "request hook changed model")
	}
	again.Model = cfg.Name
	cfg.BaseURL = "https://other.test"
	for _, err := range Stream(ctx, &http.Client{}, cfg, &again) {
		require.ErrorContains(t, err, "another endpoint")
	}
}

func TestNativeThinkingConsumerStopsBeforeSignature(t *testing.T) {
	stream := thinkingStream(
		`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"partial"}}`,
		`{"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"opaque"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"message_stop"}`,
	)
	var events []llm.StreamEvent
	processStream(strings.NewReader(stream), llm.ModelConfig{}, func(ev llm.StreamEvent, err error) bool {
		require.NoError(t, err)
		events = append(events, ev)
		return false
	})
	require.Len(t, events, 1)
	assert.Nil(t, events[0].Final)
	assert.Equal(t, "partial", events[0].Delta.ReasoningContent)
}

func TestNativeThinkingDisabledUsesProjection(t *testing.T) {
	cfg := llm.ModelConfig{Name: "claude-test", Think: llm.ThinkConfig{Enabled: true, Mode: llm.Medium}}
	var final *llm.Message
	processStream(strings.NewReader(thinkingStream(
		`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"display","signature":"opaque"}}`,
		`{"type":"content_block_stop","index":0}`,
		`{"type":"content_block_start","index":1,"content_block":{"type":"redacted_thinking","data":"encrypted"}}`,
		`{"type":"content_block_stop","index":1}`,
		`{"type":"content_block_start","index":2,"content_block":{"type":"text","text":"answer"}}`,
		`{"type":"content_block_stop","index":2}`,
		`{"type":"content_block_start","index":3,"content_block":{"type":"tool_use","id":"call|special","name":"read","input":{}}}`,
		`{"type":"content_block_stop","index":3}`,
		`{"type":"message_stop"}`,
	)), cfg, func(ev llm.StreamEvent, err error) bool {
		require.NoError(t, err)
		if ev.Final != nil {
			final = ev.Final
		}
		return true
	})
	require.NotNil(t, final)
	require.NotNil(t, final.Native)
	messages := []llm.Message{*final, {Role: llm.RoleTool, ToolCallID: "call|special", Content: "ok"}}
	for _, enabled := range []bool{true, false, true} {
		cfg.Think.Enabled = enabled
		req := BuildRequest(cfg, "", messages, nil)
		require.Len(t, req.Messages, 2)
		wire, err := json.Marshal(req)
		require.NoError(t, err)
		if enabled {
			require.NotNil(t, req.Thinking)
			assert.Equal(t, final.Native.Items, req.Messages[0].Content)
			assert.Contains(t, string(wire), `"tool_use_id":"call|special"`)
		} else {
			assert.Nil(t, req.Thinking)
			require.NotContains(t, string(wire), "thinking")
			blocks := req.Messages[0].Content.([]anthropicContentBlock)
			require.Len(t, blocks, 2)
			assert.Equal(t, "answer", blocks[0].Text)
			assert.Equal(t, normalizeToolCallID("call|special"), blocks[1].ID)
			assert.Equal(t, blocks[1].ID, req.Messages[1].Content.([]anthropicContentBlock)[0].ToolUseID)
		}
		assert.NotNil(t, messages[0].Native, "turning thinking off must not erase persisted state")
	}
}

func TestNativeThinkingTruncatedToolArguments(t *testing.T) {
	for _, thinking := range []bool{false, true} {
		t.Run(map[bool]string{false: "plain", true: "thinking"}[thinking], func(t *testing.T) {
			var events []string
			index := 0
			if thinking {
				events = append(
					events,
					`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"completed reasoning","signature":"opaque"}}`,
					`{"type":"content_block_stop","index":0}`,
				)
				index++
			}
			events = append(
				events,
				fmt.Sprintf(
					`{"type":"content_block_start","index":%d,"content_block":{"type":"text","text":"kept text"}}`,
					index,
				),
				fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, index),
			)
			index++
			for i, args := range []string{`{"path":"a.go"}`, `{"path":`} {
				events = append(
					events,
					fmt.Sprintf(
						`{"type":"content_block_start","index":%d,"content_block":{"type":"tool_use","id":"call_%d","name":"read","input":{}}}`,
						index,
						i,
					),
					fmt.Sprintf(
						`{"type":"content_block_delta","index":%d,"delta":{"type":"input_json_delta","partial_json":%q}}`,
						index,
						args,
					),
					fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, index),
				)
				index++
			}
			events = append(
				events,
				`{"type":"message_delta","delta":{"stop_reason":"max_tokens"},"usage":{"output_tokens":100}}`,
				`{"type":"message_stop"}`,
			)
			var final *llm.Message
			processStream(
				strings.NewReader(thinkingStream(events...)),
				llm.ModelConfig{},
				func(ev llm.StreamEvent, err error) bool {
					require.NoError(t, err)
					if ev.Final != nil {
						final = ev.Final
					}
					return true
				},
			)
			require.NotNil(t, final)
			assert.Nil(t, final.Native)
			assert.Equal(t, "kept text", final.Content)
			if thinking {
				assert.Equal(t, "completed reasoning", final.ReasoningContent)
			}
			require.Len(t, final.ToolCalls, 2)
			assert.Equal(t, "call_0", final.ToolCalls[0].ID)
			assert.JSONEq(t, `{"path":"a.go"}`, final.ToolCalls[0].Function.Arguments)
			assert.Equal(t, "call_1", final.ToolCalls[1].ID)
			assert.Equal(t, `{"path":`, final.ToolCalls[1].Function.Arguments)
			assert.Equal(t, 100, final.Usage.CompletionTokens)
		})
	}
}

func TestNativeThinkingIgnoresGatewayTrailer(t *testing.T) {
	for _, block := range []string{
		`{"type":"thinking","thinking":"complete","signature":"opaque"}`,
		`{"type":"redacted_thinking","data":"encrypted"}`,
		`{"type":"text","text":"complete"}`,
	} {
		t.Run(block, func(t *testing.T) {
			var final *llm.Message
			processStream(strings.NewReader(thinkingStream(
				`{"type":"content_block_start","index":0,"content_block":`+block+`}`,
				`{"type":"content_block_stop","index":0}`,
				`{"type":"message_stop"}`,
				`[DONE]`, `gateway trailer`, `{"type":"ping"}`,
			)), llm.ModelConfig{}, func(ev llm.StreamEvent, err error) bool {
				require.NoError(t, err)
				if ev.Final != nil {
					final = ev.Final
				}
				return true
			})
			require.NotNil(t, final)
			if strings.Contains(block, "thinking") {
				require.NotNil(t, final.Native)
				require.Len(t, final.Native.Items, 1)
				assert.JSONEq(t, block, string(final.Native.Items[0]))
			} else {
				assert.Equal(t, "complete", final.Content)
			}
		})
	}
}

func TestNativeThinkingOnlyFallbackKeepsUserTurns(t *testing.T) {
	cfg := llm.ModelConfig{Name: "claude-test", Think: llm.ThinkConfig{Enabled: true, Mode: llm.Medium}}
	state := &llm.NativeState{
		Version: nativeStateVersion, API: llm.Anthropic, Model: cfg.Name,
		Endpoint: endpointFingerprint(cfg.BaseURL),
		Items:    []json.RawMessage{json.RawMessage(`{"type":"thinking","thinking":"display","signature":"opaque"}`)},
	}
	other := *state
	other.Model = "other-model"
	for _, tt := range []struct {
		name    string
		state   *llm.NativeState
		enabled bool
		kept    bool
	}{
		{"legacy or unsigned", nil, true, false},
		{"model switched", &other, true, false},
		{"thinking off", state, false, false},
		{"matching signed turn", state, true, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg.Think.Enabled = tt.enabled
			messages := []llm.Message{
				{
					Role:    llm.RoleUser,
					Content: "first",
					Images:  []llm.Image{{MimeType: "image/png", Data: "opaque-image"}},
				},
				{Role: llm.RoleAssistant, ReasoningContent: "display", Native: tt.state},
				{Role: llm.RoleUser, Content: "second"},
			}
			req := BuildRequest(cfg, "", messages, nil)
			if tt.kept {
				require.Len(t, req.Messages, 3)
				assert.Equal(t, "assistant", req.Messages[1].Role)
				assert.Equal(t, state.Items, req.Messages[1].Content)
			} else {
				// The Messages API combines adjacent same-role turns; preserve their content.
				require.Len(t, req.Messages, 2)
			}
			assert.Equal(t, "user", req.Messages[0].Role)
			first := req.Messages[0].Content.([]anthropicContentBlock)
			require.Len(t, first, 2)
			assert.Equal(t, "first", first[0].Text)
			assert.Equal(t, "opaque-image", first[1].Source.Data)
			last := req.Messages[len(req.Messages)-1]
			assert.Equal(t, "user", last.Role)
			blocks := last.Content.([]anthropicContentBlock)
			require.Len(t, blocks, 1)
			assert.Equal(t, "second", blocks[0].Text)
			assert.NotNil(t, blocks[0].CacheControl)
			assert.Equal(t, tt.state, messages[1].Native)
		})
	}
}

func TestNativeThinkingBlockOrderAfterUnreplayableTool(t *testing.T) {
	for _, index := range []int{2, 1, 3} {
		t.Run(fmt.Sprintf("next index %d", index), func(t *testing.T) {
			stream := thinkingStream(
				`{"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":"complete","signature":"opaque"}}`,
				`{"type":"content_block_stop","index":0}`,
				`{"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"call_1","name":"read","input":{}}}`,
				`{"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"path\":"}}`,
				`{"type":"content_block_stop","index":1}`,
				fmt.Sprintf(
					`{"type":"content_block_start","index":%d,"content_block":{"type":"text","text":"after"}}`,
					index,
				),
				fmt.Sprintf(`{"type":"content_block_stop","index":%d}`, index),
				`{"type":"message_stop"}`,
			)
			var final *llm.Message
			var streamErr error
			processStream(strings.NewReader(stream), llm.ModelConfig{}, func(ev llm.StreamEvent, err error) bool {
				if err != nil {
					streamErr = err
				}
				if ev.Final != nil {
					final = ev.Final
				}
				return true
			})
			if index != 2 {
				require.ErrorContains(t, streamErr, "invalid block order")
				assert.Nil(t, final)
				return
			}
			require.NoError(t, streamErr)
			require.NotNil(t, final)
			assert.Nil(t, final.Native)
			assert.Equal(t, "complete", final.ReasoningContent)
			assert.Equal(t, "after", final.Content)
			require.Len(t, final.ToolCalls, 1)
			assert.Equal(t, `{"path":`, final.ToolCalls[0].Function.Arguments)
		})
	}
}
