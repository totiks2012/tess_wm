package main

import (
	"testing"
)

// TestTTYConsoleDevice proves the pure-console detection separates Linux
// console devices from emulated ptys without touching PATH/HOME.
func TestTTYConsoleDevice(t *testing.T) {
	cases := []struct {
		dev  string
		want bool
	}{
		{"/dev/tty1", true},
		{"/dev/tty63", true},
		{"/dev/vc/1", true},
		{"/dev/console", true},
		{"/dev/pts/2", false},
		{"/dev/pts/0", false},
		{"/dev/ptmx", false},
		{"", false},
	}
	for _, c := range cases {
		if got := ttyConsoleDevice(c.dev); got != c.want {
			t.Errorf("ttyConsoleDevice(%q) = %v, want %v", c.dev, got, c.want)
		}
	}
}

// TestIsGUITerminalWayland proves WAYLAND_DISPLAY alone always wins, and a
// console (after /proc isn't readable in tests, so DISPLAY+fd0 rules apply).
func TestIsGUITerminalWayland(t *testing.T) {
	t.Setenv("WAYLAND_DISPLAY", "wayland-0")
	// DISPLAY irrelevant under Wayland.
	t.Setenv("DISPLAY", ":0")
	if !isGUITerminal() {
		t.Fatal("WAYLAND_DISPLAY set => must be a GUI session")
	}
}

// TestIsGUITerminalNoDisplay proves that without any display server the app is
// keyboard-only (pure console), regardless of the underlying tty.
func TestIsGUITerminalNoDisplay(t *testing.T) {
	t.Setenv("WAYLAND_DISPLAY", "")
	t.Setenv("DISPLAY", "")
	if isGUITerminal() {
		t.Fatal("no display server => must NOT be a GUI session")
	}
}

// TestGUIBYEnvTable drives the pure classification formula across the matrix:
// Wayland always GUI; X11 is GUI only over an emulated pty; a /dev/ttyN
// console under X11 stays keyboard-only; unreadable fd0 defers to DISPLAY.
func TestGUIBYEnvTable(t *testing.T) {
	cases := []struct {
		name             string
		wayland, display string
		fd0              string
		want             bool
	}{
		{"wayland", "wayland-0", ":0", "/dev/pts/2", true},
		{"wayland even over console", "wayland-0", "", "/dev/tty1", true},
		{"headless", "", "", "/dev/pts/2", false},
		{"x11 over pty", "", ":0", "/dev/pts/2", true},
		{"x11 over pty alt", "", ":0", "/dev/pts/0", true},
		{"x11 over console", "", ":0", "/dev/tty1", false},
		{"x11 over vc", "", ":0", "/dev/vc/1", false},
		{"x11 unreadable fd0 trusts display", "", ":0", "", true},
	}
	for _, c := range cases {
		if got := guiByEnv(c.wayland, c.display, c.fd0); got != c.want {
			t.Errorf("%s: guiByEnv(%q,%q,%q) = %v, want %v", c.name, c.wayland, c.display, c.fd0, got, c.want)
		}
	}
}

// TestTermSupportsMouse proves the TERM terminfo gate behaves as tcell defines
// it: mouse-capable TERM entries (xterm family) report true, unknown entries
// report false. (Note: the real TTY-vs-GUI decision is isGUITerminal() — the
// EnableMouse gate is belt-and-suspenders only.)
func TestTermSupportsMouse(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	if !termSupportsMouse() {
		t.Fatal("xterm-256color must advertise mouse capability")
	}
	t.Setenv("TERM", "definitely-not-a-real-terminal-xyz")
	if termSupportsMouse() {
		t.Fatal("unknown TERM must fall back to no mouse")
	}
	t.Setenv("TERM", "")
	if termSupportsMouse() {
		t.Fatal("empty TERM must fall back to no mouse")
	}
}
