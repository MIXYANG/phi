// Package core is the extension kernel, mirroring cordis's core package: the
// transport, the per-extension lifecycle and the aggregation live here as
// files of one package.
//
//   - proc.go:    one Proc per extension subprocess — PXB frames over
//     stdin/stdout, the handshake (Hello → HelloAck → Register*/Subscribe →
//     Ready), request/response RPC with per-tool timeouts, and teardown
//     (graceful Shutdown, then kill).
//   - fiber.go:   Fiber, the state machine around a Proc (pending → loading →
//     active → failed/disposed), including hot swap.
//   - runner.go:  Runner, which aggregates the fibers and dispatches events.
//   - view.go:    the Runner's cached read model (tools, commands, handlers).
//   - manage.go:  the Runner's per-extension topology mutations (load, unload,
//     reload, state queries).
//
// The transport half (Proc) stays dumb: callers pass a HostSink to receive
// spontaneous frames and read registrations back through Tools/Commands. The
// Runner half knows about phi's tools and the SDK types; the loader below it
// knows nothing about any of this.
package core
