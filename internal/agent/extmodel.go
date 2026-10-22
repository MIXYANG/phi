package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"slices"
	"strings"

	"github.com/pulseaiclub/phi/ext/go/pxb"
	"github.com/pulseaiclub/phi/internal/extension"
	"github.com/pulseaiclub/phi/internal/llm"
)

// chatClient is the transport the agent loop drives a turn with: the builtin
// HTTP client (*llmclient.Client) or an extension-served model connection.
type chatClient interface {
	Stream(ctx context.Context, messages []llm.Message) iter.Seq2[llm.StreamEvent, error]
	// Compact satisfies llm.Compactor for session compaction.
	Compact(ctx context.Context, req llm.CompactRequest) (llm.CompactResult, error)
}

// modelStreamFn starts one extension model stream. *extension.Runner implements
// it; tests inject fakes.
type modelStreamFn func(
	ctx context.Context,
	route extension.ModelRoute,
	req pxb.ModelInvoke,
) (iter.Seq2[pxb.ModelStreamEvent, error], error)

// extModelClient adapts an extension-served model (RegisterModel Conn "ext")
// to chatClient. The extension owns the connection — proxy, gateway, or local
// generation; the host serializes the provider-neutral request and the
// extension answers with ModelStreamEvent frames that this client folds back
// into llm.StreamEvent, assembling the final assistant message from deltas.
type extModelClient struct {
	stream modelStreamFn
	route  extension.ModelRoute
	cfg    llm.ModelConfig
	tools  []llm.ToolDefinition
	system string
}

func newExtModelClient(
	runner *extension.Runner,
	route extension.ModelRoute,
	cfg llm.ModelConfig,
	tools []llm.ToolDefinition,
	system string,
) *extModelClient {
	return newExtModelClientWithStream(runner.ModelStream, route, cfg, tools, system)
}

func newExtModelClientWithStream(
	stream modelStreamFn,
	route extension.ModelRoute,
	cfg llm.ModelConfig,
	tools []llm.ToolDefinition,
	system string,
) *extModelClient {
	return &extModelClient{stream: stream, route: route, cfg: cfg, tools: tools, system: system}
}

// Stream runs one chat completion on the extension. Deltas pass through; the
// done frame's final message is assembled here so extensions never re-implement
// accumulation. A stream that ends without done is a transport error.
func (c *extModelClient) Stream(
	ctx context.Context,
	messages []llm.Message,
) iter.Seq2[llm.StreamEvent, error] {
	return func(yield func(llm.StreamEvent, error) bool) {
		invoke, err := c.buildInvoke(messages)
		if err != nil {
			yield(llm.StreamEvent{}, err)
			return
		}
		frames, err := c.stream(ctx, c.route, invoke)
		if err != nil {
			yield(llm.StreamEvent{}, err)
			return
		}
		acc := newStreamAccumulator()
		for ev, ferr := range frames {
			if ferr != nil {
				yield(llm.StreamEvent{}, ferr)
				return
			}
			switch ev.Type {
			case "delta":
				delta, derr := decodeModelDelta(ev)
				if derr != nil {
					yield(llm.StreamEvent{}, derr)
					return
				}
				if uerr := acc.applyUsage(ev); uerr != nil {
					yield(llm.StreamEvent{}, uerr)
					return
				}
				acc.apply(delta)
				if delta.Content != "" || delta.ReasoningContent != "" || len(delta.ToolCalls) > 0 {
					if !yield(llm.StreamEvent{Type: llm.StreamEventTypeDelta, Delta: delta, Usage: acc.usage}, nil) {
						return
					}
				}
			case "error":
				text := ev.Error
				if text == "" {
					text = fmt.Sprintf("extension model %q stream failed", c.route.Reg.Name)
				}
				yield(llm.StreamEvent{}, errors.New(text))
				return
			case "done":
				if uerr := acc.applyUsage(ev); uerr != nil {
					yield(llm.StreamEvent{}, uerr)
					return
				}
				msg := acc.message()
				yield(llm.StreamEvent{Type: llm.StreamEventTypeDone, Final: &msg}, nil)
				return
			default:
				yield(
					llm.StreamEvent{},
					fmt.Errorf("extension model %q: unknown stream event %q", c.route.Reg.Name, ev.Type),
				)
				return
			}
		}
		// Iterator ended without done: a cancelled context surfaces as a
		// transport error like the HTTP providers do; anything else is a
		// protocol violation.
		if ctx.Err() != nil {
			yield(llm.StreamEvent{}, ctx.Err())
			return
		}
		yield(llm.StreamEvent{}, errors.New("extension model stream closed without done"))
	}
}

// Compact summarizes by streaming a single-user-message completion — the
// extension sees an ordinary (tool-less, system-less) chat request, so no
// dedicated wire path is needed. The Truncated flag is never set: extensions
// have no output-cap signal. MaxTokens is advisory only, like the prompt.
func (c *extModelClient) Compact(ctx context.Context, req llm.CompactRequest) (llm.CompactResult, error) {
	invoke, err := buildInvoke(
		c.route.Reg.Name, nil, "", "", []llm.Message{{Role: llm.RoleUser, Content: req.Prompt}},
	)
	if err != nil {
		return llm.CompactResult{}, err
	}
	frames, err := c.stream(ctx, c.route, invoke)
	if err != nil {
		return llm.CompactResult{}, err
	}
	var text strings.Builder
	for ev, ferr := range frames {
		if ferr != nil {
			return llm.CompactResult{}, ferr
		}
		switch ev.Type {
		case "delta":
			text.WriteString(ev.Content)
		case "error":
			msg := ev.Error
			if msg == "" {
				msg = "extension model compaction failed"
			}
			return llm.CompactResult{}, errors.New(msg)
		case "done":
			return llm.CompactResult{Text: text.String()}, nil
		}
	}
	return llm.CompactResult{}, errors.New("extension model compaction closed without done")
}

// buildInvoke serializes the neutral request. Messages reuse the session
// serialization JSON tags, so extensions can decode conversations without
// importing host internals.
func (c *extModelClient) buildInvoke(messages []llm.Message) (pxb.ModelInvoke, error) {
	think := ""
	if c.cfg.Think.Enabled {
		think = string(c.cfg.Think.Mode)
	}
	return buildInvoke(c.route.Reg.Name, c.tools, c.system, think, messages)
}

func buildInvoke(
	model string,
	tools []llm.ToolDefinition,
	system string,
	think string,
	messages []llm.Message,
) (pxb.ModelInvoke, error) {
	msgs, err := json.Marshal(messages)
	if err != nil {
		return pxb.ModelInvoke{}, fmt.Errorf("encode messages for extension model: %w", err)
	}
	var toolsJSON []byte
	if len(tools) > 0 {
		toolsJSON, err = json.Marshal(tools)
		if err != nil {
			return pxb.ModelInvoke{}, fmt.Errorf("encode tools for extension model: %w", err)
		}
	}
	return pxb.ModelInvoke{
		Model:        model,
		MessagesJSON: msgs,
		ToolsJSON:    toolsJSON,
		SystemPrompt: system,
		Think:        think,
	}, nil
}

// decodeModelDelta converts one wire frame to a neutral delta. Tool call
// fragments follow the OpenAI streaming convention the extension SDKs mirror.
func decodeModelDelta(ev pxb.ModelStreamEvent) (llm.StreamDelta, error) {
	delta := llm.StreamDelta{Content: ev.Content, ReasoningContent: ev.Reasoning}
	if len(ev.ToolCalls) > 0 {
		if err := json.Unmarshal(ev.ToolCalls, &delta.ToolCalls); err != nil {
			return llm.StreamDelta{}, fmt.Errorf("decode extension tool call delta: %w", err)
		}
	}
	return delta, nil
}

// extStreamAccumulator folds deltas into the final assistant message. Tool
// calls arrive as fragments keyed by Index: the first fragment carries
// ID/Type/Name, later ones append argument text.
type extStreamAccumulator struct {
	content   strings.Builder
	reasoning strings.Builder
	calls     map[int]*llm.ToolCall
	order     []int
	usage     llm.Usage
}

func newStreamAccumulator() *extStreamAccumulator {
	return &extStreamAccumulator{calls: make(map[int]*llm.ToolCall)}
}

func (a *extStreamAccumulator) apply(delta llm.StreamDelta) {
	a.content.WriteString(delta.Content)
	a.reasoning.WriteString(delta.ReasoningContent)
	for _, tc := range delta.ToolCalls {
		cur, ok := a.calls[tc.Index]
		if !ok {
			cp := tc
			if cp.Type == "" {
				cp.Type = "function"
			}
			cur = &cp
			a.calls[tc.Index] = cur
			a.order = append(a.order, tc.Index)
			continue
		}
		if tc.ID != "" {
			cur.ID = tc.ID
		}
		if tc.Type != "" {
			cur.Type = tc.Type
		}
		if tc.Function.Name != "" && cur.Function.Name == "" {
			cur.Function.Name = tc.Function.Name
		}
		cur.Function.Arguments += tc.Function.Arguments
	}
}

// applyUsage records the latest reported usage; extensions may send it on a
// delta or on the done frame.
func (a *extStreamAccumulator) applyUsage(ev pxb.ModelStreamEvent) error {
	if len(ev.UsageJSON) == 0 {
		return nil
	}
	if err := json.Unmarshal(ev.UsageJSON, &a.usage); err != nil {
		return fmt.Errorf("decode extension usage: %w", err)
	}
	return nil
}

func (a *extStreamAccumulator) message() llm.Message {
	msg := llm.Message{
		Role:             llm.RoleAssistant,
		Content:          a.content.String(),
		ReasoningContent: a.reasoning.String(),
		Usage:            a.usage,
	}
	order := append([]int(nil), a.order...)
	slices.Sort(order)
	for _, idx := range order {
		msg.ToolCalls = append(msg.ToolCalls, *a.calls[idx])
	}
	return msg
}
