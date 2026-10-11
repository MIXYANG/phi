package main

import (
	"os"
	"strconv"
	"strings"

	"github.com/pulseaiclub/xui"
)

func newTerminalUI() (*xui.XUI, error) {
	vx, err := xui.New(xui.Options{Mouse: true, BracketedPaste: true})
	if err != nil {
		return nil, err
	}
	if windowsTerminalColorHintAllowed() {
		vx.SetColorLevel(xui.ColorTrue)
	}
	return vx, nil
}

// xui falls back to basic colors when TERM/COLORTERM are absent, as they often
// are in PowerShell. WT_SESSION identifies a truecolor-capable Windows Terminal;
// preserve xui's explicit color ceilings before applying that missing hint.
// The env rules below mirror xui's unexported detectColorLevel (caps.go); once
// xui detects WT_SESSION itself or exposes its "hard ceiling" flag, this mirror
// and newTerminalUI's override should shrink to just the WT_SESSION check.
func windowsTerminalColorHintAllowed() bool {
	if strings.TrimSpace(os.Getenv("WT_SESSION")) == "" {
		return false
	}
	if forced, ok := os.LookupEnv("FORCE_COLOR"); ok {
		switch strings.ToLower(strings.TrimSpace(forced)) {
		case "", "false", "0", "1", "2", "3":
			return false
		}
		// Match xui: normalize known literals, but parse other numbers verbatim.
		_, err := strconv.Atoi(forced)
		// Numeric values set a hard ceiling; true or unrecognized values only
		// enable colors, so a terminal hint may still raise the color depth.
		return err != nil
	}
	if _, disabled := os.LookupEnv("NO_COLOR"); disabled {
		return false
	}
	return os.Getenv("TERM") != "dumb"
}
