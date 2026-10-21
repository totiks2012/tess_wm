package main

import (
	"os"
	"strings"

	"github.com/gdamore/tcell/v2/terminfo"
)

// isGUITerminal reports whether the app runs under a graphical display server
// rather than a pure Linux console. Deterministic: Wayland is signalled by
// WAYLAND_DISPLAY; under X11 DISPLAY is set AND stdin is a terminal emulator
// pty (not a /dev/ttyN console). In every other case (pure TTY, headless) it
// is false, which keeps the app keyboard-only.
func isGUITerminal() bool {
	wayland := os.Getenv("WAYLAND_DISPLAY")
	display := os.Getenv("DISPLAY")
	dev, err := os.Readlink("/proc/self/fd/0")
	if err != nil {
		dev = ""
	}
	return guiByEnv(wayland, display, dev)
}

// guiByEnv applies the TTY-vs-GUI classification formula to explicit inputs so
// it can be tested without touching the real /proc or env.
func guiByEnv(wayland, display, fd0 string) bool {
	if wayland != "" {
		return true
	}
	if display == "" {
		return false
	}
	if fd0 == "" {
		// Cannot inspect the tty: trust DISPLAY.
		return true
	}
	return !ttyConsoleDevice(fd0)
}

// ttyConsoleDevice reports whether the given device path is a Linux console
// tty (as opposed to an emulated pty).
func ttyConsoleDevice(dev string) bool {
	for _, prefix := range []string{"/dev/tty", "/dev/vc/", "/dev/console"} {
		if strings.HasPrefix(dev, prefix) {
			return true
		}
	}
	return false
}

// termSupportsMouse reports whether the TERM terminfo entry defines mouse
// capability — the same check tcell's screen.HasMouse() performs. In a Linux
// console TERM=linux has none, so even a stray EnableMouse would be inert.
func termSupportsMouse() bool {
	ti, err := terminfo.LookupTerminfo(os.Getenv("TERM"))
	if err != nil {
		return false
	}
	return ti.Mouse != ""
}
