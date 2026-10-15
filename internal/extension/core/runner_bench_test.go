package core

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	ext "github.com/pulseaiclub/phi/ext/go"

	"github.com/pulseaiclub/phi/internal/extension/loader"
	"github.com/pulseaiclub/phi/internal/tools/tooldef"
)

// benchRunner builds a runner with n active fibers, each holding one tool_call
// handler, one tool and one slash command. No processes are started: handler
// registration is write-once before Ready, so a hand-built API is
// indistinguishable from a handshaken one for the read paths under test.
func benchRunner(n int) *Runner {
	fibers := make(map[string]*Fiber, n)
	for i := range n {
		api := ext.NewAPI()
		api.On(ext.EventToolCall, func(ext.ToolCallEvent, *ext.Context) *ext.ToolCallResult {
			return nil
		})
		api.RegisterTool(ext.Tool{
			Name:        fmt.Sprintf("tool%d", i),
			Description: "bench",
			Execute: func(context.Context, json.RawMessage) (ext.ToolResult, error) {
				return ext.ToolResult{}, nil
			},
		})
		api.RegisterCommand(fmt.Sprintf("cmd%d", i), ext.Command{
			Description: "bench",
			Handler:     func(string, *ext.Context) error { return nil },
		})

		name := fmt.Sprintf("ext%02d", i)
		f := NewFiber(name, loader.Discovered{Manifest: loader.Manifest{Name: name}})
		f.Attach(api, FiberActive)
		fibers[name] = f
	}
	return &Runner{fibers: fibers, baseTools: []tooldef.Tool{}}
}

func benchmarkHandlers(b *testing.B, fibers int) {
	r := benchRunner(fibers)
	event := ext.EventToolCall

	// Sanity: the walk must find exactly one handler per fiber, else the
	// benchmark measures an empty loop.
	if got := len(r.handlers(event)); got != fibers {
		b.Fatalf("handlers(%s) = %d, want %d", event, got, fibers)
	}

	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if got := len(r.handlers(event)); got != fibers {
			b.Fatalf("handlers(%s) = %d, want %d", event, got, fibers)
		}
	}
}

// BenchmarkHandlersDispatch is the per-event dispatch walk, hit four times per
// tool call (PreTool, PostTool, execution start/end).
func BenchmarkHandlersDispatch(b *testing.B) {
	for _, fibers := range []int{0, 1, 8} {
		b.Run(fmt.Sprintf("fibers=%d", fibers), func(b *testing.B) {
			benchmarkHandlers(b, fibers)
		})
	}
}

// BenchmarkExtensionTools is the tool-schema rebuild path (engine rebind).
func BenchmarkExtensionTools(b *testing.B) {
	r := benchRunner(8)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if len(r.ExtensionTools()) == 0 {
			b.Fatal("no tools")
		}
	}
}

// BenchmarkCommandEntries is the palette/slash-command rebuild path.
func BenchmarkCommandEntries(b *testing.B) {
	r := benchRunner(8)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if len(r.CommandEntries()) == 0 {
			b.Fatal("no commands")
		}
	}
}

// BenchmarkDispatchContext builds the per-dispatch ext.Context.
func BenchmarkDispatchContext(b *testing.B) {
	r := benchRunner(1)
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if r.context() == nil {
			b.Fatal("nil context")
		}
	}
}
