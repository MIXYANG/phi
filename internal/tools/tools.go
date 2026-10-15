package tools

import (
	"github.com/pulseaiclub/phi/internal/tools/bashtool"
	"github.com/pulseaiclub/phi/internal/tools/edittool"
	"github.com/pulseaiclub/phi/internal/tools/findtool"
	"github.com/pulseaiclub/phi/internal/tools/greptool"
	"github.com/pulseaiclub/phi/internal/tools/lstool"
	"github.com/pulseaiclub/phi/internal/tools/readtool"
	"github.com/pulseaiclub/phi/internal/tools/tooldef"
	"github.com/pulseaiclub/phi/internal/tools/writetool"
)

// DefaultTools returns the built-in agent tool set.
func DefaultTools() []tooldef.Tool {
	return []tooldef.Tool{
		readtool.ReadTool(),
		greptool.GrepTool(),
		findtool.FindTool(),
		lstool.LsTool(),
		edittool.EditTool(),
		bashtool.BashTool(),
		writetool.WriteTool(),
	}
}

// ReadonlyTools returns exploration tools without write/edit.
// Bash remains registered; pair with ModeReadonly (and typically
// ChildPolicy) so write/edit stay denied while non-deny bash is allowed.
func ReadonlyTools() []tooldef.Tool {
	return []tooldef.Tool{
		bashtool.BashTool(),
		readtool.ReadTool(),
		greptool.GrepTool(),
		lstool.LsTool(),
		findtool.FindTool(),
	}
}
