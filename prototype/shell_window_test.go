package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"
	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// TestShellWindowResizeWriteRace proves that concurrent pty output (background
// Write goroutine) and Draw()+Resize (tview main thread) do not race. Run with
// `go test -race ./...`.
func TestShellWindowResizeWriteRace(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not found")
	}
	app := tview.NewApplication()
	sw, err := newShellWindow(app, "", nil, func() bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	defer sw.close()

	sim := tcell.NewSimulationScreen("UTF-8")
	if err := sim.Init(); err != nil {
		t.Fatal(err)
	}
	defer sim.Fini()
	sim.SetSize(100, 40)

	// Make bash produce a constant stream of output while we re-render/resize.
	if _, err := sw.ptmx.Write([]byte("yes WMTV_RACE_OUTPUT\n")); err != nil {
		t.Fatal(err)
	}

	sizes := []struct{ w, h int }{{100, 40}, {80, 24}, {120, 30}, {60, 20}, {100, 40}}
	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			s := sizes[i%len(sizes)]
			sw.SetRect(0, 0, s.w, s.h)
			sw.Draw(sim)
		}
	}()
	time.Sleep(500 * time.Millisecond)
	close(stop)
	wg.Wait()
}

// TestKeyBytesFunctionKeys proves F-keys are translated to their terminal
// escape sequences, so passed-through F-keys reach programs inside the shell.
func TestKeyBytesFunctionKeys(t *testing.T) {
	cases := []struct {
		key  tcell.Key
		want string
	}{
		{tcell.KeyF1, "\x1bOP"},
		{tcell.KeyF2, "\x1bOQ"},
		{tcell.KeyF3, "\x1bOR"},
		{tcell.KeyF4, "\x1bOS"},
		{tcell.KeyF5, "\x1b[15~"},
		{tcell.KeyF6, "\x1b[17~"},
		{tcell.KeyF7, "\x1b[18~"},
		{tcell.KeyF8, "\x1b[19~"},
		{tcell.KeyF9, "\x1b[20~"},
		{tcell.KeyF10, "\x1b[21~"},
		{tcell.KeyF11, "\x1b[23~"},
		{tcell.KeyF12, "\x1b[24~"},
	}
	for _, c := range cases {
		got := string(keyBytes(tcell.NewEventKey(c.key, 0, tcell.ModNone)))
		if got != c.want {
			t.Errorf("keyBytes(%v) = %q, want %q", c.key, got, c.want)
		}
	}
}

// TestShellShouldRedraw proves the Task-2 throttle: only windows that want a
// redraw (active, or all while the overview is open) trigger UI redraws;
// background shells just buffer.
func TestShellShouldRedraw(t *testing.T) {
	w := &shellWindow{}
	if w.shouldRedraw() {
		t.Fatal("no wantsRedraw callback -> must not redraw")
	}

	wantsRedraw := false
	w.wantsRedraw = func() bool { return wantsRedraw }
	if w.shouldRedraw() {
		t.Fatal("inactive window must not redraw")
	}

	wantsRedraw = true
	if !w.shouldRedraw() {
		t.Fatal("active window must redraw")
	}

	w.closed = true
	if w.shouldRedraw() {
		t.Fatal("closed window must not redraw")
	}
}

// TestWMFKeyPassThrough proves the F-key gating: a full-screen TUI in the
// focused shell owns plain F-keys, Alt+Fn is always a WM command, and at an
// ordinary prompt the plain F-keys are WM commands.
func TestWMFKeyPassThrough(t *testing.T) {
	f2 := tcell.NewEventKey(tcell.KeyF2, 0, tcell.ModNone)
	f2Alt := tcell.NewEventKey(tcell.KeyF2, 0, tcell.ModAlt)
	f4 := tcell.NewEventKey(tcell.KeyF4, 0, tcell.ModNone)

	if !wmFKeyPassThrough(f2, true) {
		t.Fatal("full-screen TUI focused: plain F must pass through to the program")
	}
	if !wmFKeyPassThrough(f4, true) {
		t.Fatal("full-screen TUI focused: plain F must pass through (htop needs F4)")
	}
	if wmFKeyPassThrough(f2Alt, true) {
		t.Fatal("full-screen TUI focused: Alt+F must be a WM command")
	}
	if wmFKeyPassThrough(f2, false) {
		t.Fatal("no full-screen TUI: plain F must be a WM command")
	}
}

// TestF12ModeToggle proves the manual F-key policy switch: F12 is always the
// WM's, auto passes F-keys through only in a full-screen TUI, forced-WM passes
// nothing through.
func TestF12ModeToggle(t *testing.T) {
	app := tview.NewApplication()
	wm := newWM(app, tview.NewBox())

	if wm.fKeyForceWMRead() {
		t.Fatal("default mode must be auto")
	}
	sw := &shellWindow{emu: vt.NewSafeEmulator(80, 24)}
	wm.addWindow("shell", sw, sw)

	if wm.shouldPassFKey(tcell.NewEventKey(tcell.KeyF12, 0, tcell.ModNone)) {
		t.Fatal("F12 must always be controlled by the WM")
	}

	wm.ToggleFKeyMode()
	if !wm.fKeyForceWMRead() {
		t.Fatal("toggle must enable forced-WM")
	}
	if wm.shouldPassFKey(tcell.NewEventKey(tcell.KeyF2, 0, tcell.ModNone)) {
		t.Fatal("forced-WM: F2 must not pass through even in a full-screen TUI")
	}
	if wm.FKeyModeLabel() != "F12: WM" {
		t.Fatalf("label = %q, want %q", wm.FKeyModeLabel(), "F12: WM")
	}

	wm.ToggleFKeyMode()
	if wm.fKeyForceWMRead() {
		t.Fatal("second toggle must return to auto")
	}

	// auto + full-screen TUI passes ordinary F-keys through, F12 stays WM's.
	_, _ = sw.emu.Write([]byte("\x1b[?1049h"))
	if !wm.shouldPassFKey(tcell.NewEventKey(tcell.KeyF2, 0, tcell.ModNone)) {
		t.Fatal("auto + full-screen: F2 must pass through")
	}
	if wm.shouldPassFKey(tcell.NewEventKey(tcell.KeyF12, 0, tcell.ModNone)) {
		t.Fatal("auto + full-screen: F12 must still be controlled by the WM")
	}
}

// TestWMFKeyGateAltScreen proves the end-to-end gate decision uses the VT
// emulator's alt-screen state: prompt -> WM commands, full-screen program ->
// passthrough.
func TestWMFKeyGateAltScreen(t *testing.T) {
	app := tview.NewApplication()
	wm := newWM(app, tview.NewBox())

	if wmFKeyPassThrough(tcell.NewEventKey(tcell.KeyF2, 0, tcell.ModNone), wm.activeShellAltScreen()) {
		t.Fatal("no windows: F2 must be a WM command")
	}

	sw := &shellWindow{emu: vt.NewSafeEmulator(80, 24)}
	wm.addWindow("shell", sw, sw)

	if wm.activeShellAltScreen() {
		t.Fatal("bash prompt must not be treated as full-screen")
	}
	if wmFKeyPassThrough(tcell.NewEventKey(tcell.KeyF2, 0, tcell.ModNone), wm.activeShellAltScreen()) {
		t.Fatal("at the prompt F2 must be a WM command (the reported bug)")
	}

	// A full-screen TUI switches the emulator to the alternate screen.
	if _, err := sw.emu.Write([]byte("\x1b[?1049h")); err != nil {
		t.Fatal(err)
	}
	if !wm.activeShellAltScreen() {
		t.Fatal("alt-screen state must be detected")
	}
	if !wmFKeyPassThrough(tcell.NewEventKey(tcell.KeyF2, 0, tcell.ModNone), wm.activeShellAltScreen()) {
		t.Fatal("in a full-screen TUI plain F2 must go into the program")
	}
	if wmFKeyPassThrough(tcell.NewEventKey(tcell.KeyF2, 0, tcell.ModAlt), wm.activeShellAltScreen()) {
		t.Fatal("in a full-screen TUI Alt+F2 must be a WM command")
	}

	// Leaving the full-screen program returns the emulator to the prompt.
	if _, err := sw.emu.Write([]byte("\x1b[?1049l")); err != nil {
		t.Fatal(err)
	}
	if wmFKeyPassThrough(tcell.NewEventKey(tcell.KeyF2, 0, tcell.ModNone), wm.activeShellAltScreen()) {
		t.Fatal("after leaving a full-screen TUI F2 must be a WM command again")
	}
}

// TestShellWindowPastePrimaryEndToEnd proves the whole paste path against a
// real bash: a fake `xclip` (an isolated tmpdir stub on a PATH override, never
// touching system directories) provides the PRIMARY selection, pastePrimary
// injects it through the VT emulator into the pty, and bash executes it — the
// output becomes visible in the VT buffer.
func TestShellWindowPastePrimaryEndToEnd(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not found")
	}

	sandbox := t.TempDir()
	stub := filepath.Join(sandbox, "xclip")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nprintf 'echo WMTV_PASTE_OK\\n'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", sandbox+string(os.PathListSeparator)+os.Getenv("PATH"))

	// wantsRedraw=false keeps the pty-read goroutine from calling
	// QueueUpdateDraw: there is no tview event loop in a unit test, so that
	// channel would never be drained and the goroutine would block after the
	// first output chunk (a test-harness artifact — the running app drains it).
	app := tview.NewApplication()
	sw, err := newShellWindow(app, "", nil, func() bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	defer sw.close()

	sw.pastePrimary()

	deadline := time.Now().Add(5 * time.Second)
	for {
		if text := shellBufferText(sw); strings.Contains(text, "WMTV_PASTE_OK") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("pasted text never executed in bash; buffer:\n%s", shellBufferText(sw))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// shellBufferText renders the VT buffer rows of a shell window into one string
// (test helper, held under the window lock).
func shellBufferText(sw *shellWindow) string {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	ew, eh := sw.emu.Width(), sw.emu.Height()
	var sb strings.Builder
	for y := 0; y < eh; y++ {
		for x := 0; x < ew; x++ {
			if c := sw.emu.CellAt(x, y); c != nil {
				sb.WriteString(c.Content)
			} else {
				sb.WriteByte(' ')
			}
		}
		sb.WriteByte('\n')
	}
	return sb.String()
}

// TestShellWindowMiddlePasteHandler proves the middle mouse button inside the
// shell window is consumed (the pick-up point for PRIMARY paste), clicks
// outside are not, and left-click keeps the Box default focus behavior.
func TestShellWindowMiddlePasteHandler(t *testing.T) {
	w := &shellWindow{
		Box: tview.NewBox(),
		emu: vt.NewSafeEmulator(80, 24),
	}
	w.SetRect(0, 0, 20, 8)
	h := w.MouseHandler()
	if h == nil {
		t.Fatal("MouseHandler must not be nil")
	}

	consumed, _ := h(tview.MouseMiddleClick, tcell.NewEventMouse(5, 4, tcell.ButtonMiddle, tcell.ModNone), nil)
	if !consumed {
		t.Fatal("middle click inside the window must be consumed by the shell")
	}

	consumed, _ = h(tview.MouseMiddleDown, tcell.NewEventMouse(5, 4, tcell.ButtonMiddle, tcell.ModNone), nil)
	if !consumed {
		t.Fatal("middle press inside the window must be consumed by the shell")
	}

	consumed, _ = h(tview.MouseMiddleClick, tcell.NewEventMouse(50, 50, tcell.ButtonMiddle, tcell.ModNone), nil)
	if consumed {
		t.Fatal("middle click outside the window must not be consumed")
	}

	consumed, _ = h(tview.MouseLeftDown, tcell.NewEventMouse(5, 4, tcell.ButtonPrimary, tcell.ModNone), func(tview.Primitive) {})
	if !consumed {
		t.Fatal("left click inside must keep the Box default consumption")
	}
}

// TestNormalizePasteNewlines proves pasted text line breaks become CR (Enter),
// faithfully mirroring what a terminal does when injecting a selection into a
// tty.
func TestNormalizePasteNewlines(t *testing.T) {
	cases := []struct{ in, want string }{
		{"abc", "abc"},
		{"a\nb", "a\rb"},
		{"a\r\nb", "a\rb"},
		{"line1\nline2\n", "line1\rline2\r"},
	}
	for _, c := range cases {
		if got := normalizePasteNewlines(c.in); got != c.want {
			t.Errorf("normalizePasteNewlines(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestWMActiveIsShell proves the Task-3 focus gating: plain F-keys belong to a
// focused shell, not to the WM.
func TestWMActiveIsShell(t *testing.T) {
	app := tview.NewApplication()
	wm := newWM(app, tview.NewBox())

	if wm.activeIsShell() {
		t.Fatal("empty WM must not report shell focus")
	}
	wm.openWidget()
	if wm.activeIsShell() {
		t.Fatal("widget window must not count as shell focus")
	}
	wm.openShellAt("/tmp")
	if !wm.activeIsShell() {
		t.Fatal("shell window must count as shell focus")
	}
	wm.closeActive()
	if wm.activeIsShell() {
		t.Fatal("after closing the shell there must be no shell focus")
	}
}

// stubXclip installs (in an isolated tmpdir, PATH override) a fake xclip that
// both serves clipboard reads (`-o -selection <s>`) and writes (`-selection
// <s>`) from/to files under "dir", simulating CLIPBOARD/PRIMARY buffers.
// Returns dir. Never touches system directories.
func stubXclip(t *testing.T) string {
	t.Helper()
	sandbox := t.TempDir()
	stub := filepath.Join(sandbox, "xclip")
	script := `#!/bin/sh
OUT="$XDG_STUB_OUT"
readmode=0
sel=clipboard
while [ $# -gt 0 ]; do
	case "$1" in
		-o) readmode=1 ;;
		-selection) sel="$2"; shift ;;
		*) sel="$1" ;;
	esac
	shift
done
if [ "$readmode" = 1 ]; then
	cat "$OUT/$sel.txt" 2>/dev/null
else
	cat > "$OUT/$sel.txt"
fi
`
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", sandbox+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("XDG_STUB_OUT", sandbox)
	t.Setenv("WAYLAND_DISPLAY", "")
	return sandbox
}

func pressKey(h func(*tcell.EventKey, func(tview.Primitive)), key tcell.Key, r rune, mod tcell.ModMask) {
	if h == nil {
		return
	}
	h(tcell.NewEventKey(key, r, mod), nil)
}

// TestCopyModeSelectionAndCopy proves the full keyboard path: F5 copy mode,
// hjkl/arrow navigation, Space pins the anchor, Enter copies the whole region
// into the clipboard (stubbed xclip) and exits copy mode.
func TestCopyModeSelectionAndCopy(t *testing.T) {
	sandbox := stubXclip(t)

	sw := &shellWindow{Box: tview.NewBox(), emu: vt.NewSafeEmulator(80, 24)}
	h := sw.InputHandler()
	if _, err := sw.emu.Write([]byte("hello world\r\nsecond line\r\n")); err != nil {
		t.Fatal(err)
	}

	sw.ToggleCopyMode()
	if !sw.inCopyMode() {
		t.Fatal("copy mode must be active after toggle")
	}

	// cursor was left at (0,2); move up to row 0, home to column 0.
	pressKey(h, tcell.KeyUp, 0, tcell.ModNone)
	pressKey(h, tcell.KeyUp, 0, tcell.ModNone)
	pressKey(h, tcell.KeyHome, 0, tcell.ModNone)

	// Space pins the anchor, Right(4) extends to column 4 => "hello".
	pressKey(h, tcell.KeyRune, ' ', tcell.ModNone)
	for range 4 {
		pressKey(h, tcell.KeyRight, 0, tcell.ModNone)
	}

	pressKey(h, tcell.KeyEnter, 0, tcell.ModNone)

	if sw.inCopyMode() {
		t.Fatal("copy mode must exit after Enter")
	}

	want := "hello"
	deadline := time.Now().Add(3 * time.Second)
	for {
		b, err := os.ReadFile(filepath.Join(sandbox, "clipboard.txt"))
		if err == nil && strings.TrimRight(string(b), "\n") == want {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("clipboard.txt = %q, want %q", string(b), want)
		}
		time.Sleep(30 * time.Millisecond)
	}
}

// TestCopyModeEnterWithoutSelection proves Enter without a pinned anchor exits
// copy mode but writes nothing to the clipboard.
func TestCopyModeEnterWithoutSelection(t *testing.T) {
	sandbox := stubXclip(t)

	sw := &shellWindow{Box: tview.NewBox(), emu: vt.NewSafeEmulator(80, 24)}
	h := sw.InputHandler()
	sw.ToggleCopyMode()
	pressKey(h, tcell.KeyEnter, 0, tcell.ModNone)

	if sw.inCopyMode() {
		t.Fatal("copy mode must exit after bare Enter")
	}
	time.Sleep(100 * time.Millisecond) // give the async copy a chance to (not) happen
	if _, err := os.Stat(filepath.Join(sandbox, "clipboard.txt")); !os.IsNotExist(err) {
		t.Fatal("no clipboard file must be written without a selection")
	}
}

// TestCopyModeQExits proves q and Esc leave copy mode without copying.
func TestCopyModeQExits(t *testing.T) {
	sw := &shellWindow{Box: tview.NewBox(), emu: vt.NewSafeEmulator(80, 24)}
	h := sw.InputHandler()
	sw.ToggleCopyMode()
	pressKey(h, tcell.KeyRune, 'q', tcell.ModNone)
	if sw.inCopyMode() {
		t.Fatal("q must exit copy mode")
	}
	sw.ToggleCopyMode()
	pressKey(h, tcell.KeyEscape, 0, tcell.ModNone)
	if sw.inCopyMode() {
		t.Fatal("Esc must exit copy mode")
	}
}

// TestCtrlInsertRecopiesSelection proves Ctrl+Insert copies the last committed
// selection after copy mode has been left.
func TestCtrlInsertRecopiesSelection(t *testing.T) {
	sandbox := stubXclip(t)

	sw := &shellWindow{Box: tview.NewBox(), emu: vt.NewSafeEmulator(80, 24)}
	h := sw.InputHandler()
	if _, err := sw.emu.Write([]byte("alpha beta\r\n")); err != nil {
		t.Fatal(err)
	}
	sw.ToggleCopyMode()
	pressKey(h, tcell.KeyUp, 0, tcell.ModNone) // row0 (was on line1)
	pressKey(h, tcell.KeyHome, 0, tcell.ModNone)
	pressKey(h, tcell.KeyRune, ' ', tcell.ModNone)
	for range 5 {
		pressKey(h, tcell.KeyRight, 0, tcell.ModNone)
	}
	pressKey(h, tcell.KeyEnter, 0, tcell.ModNone) // copied "alpha"

	sw.copySelection() // Ctrl+Insert path

	deadline := time.Now().Add(3 * time.Second)
	for {
		b, err := os.ReadFile(filepath.Join(sandbox, "clipboard.txt"))
		if err == nil && strings.TrimRight(string(b), "\n") == "alpha" {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("clipboard.txt = %q, want `alpha`", string(b))
		}
		time.Sleep(30 * time.Millisecond)
	}
}

// TestShiftInsertPastesClipboard proves Shift+Insert reads the CLIPBOARD buffer
// (stubbed xclip -o) and injects it into a real bash, which executes it.
func TestShiftInsertPastesClipboard(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not found")
	}
	sandbox := stubXclip(t)
	if err := os.WriteFile(filepath.Join(sandbox, "clipboard.txt"), []byte("echo WMTV_PASTE_INS\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	app := tview.NewApplication()
	sw, err := newShellWindow(app, "", nil, func() bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	defer sw.close()

	sw.pasteClipboard() // Shift+Insert path

	deadline := time.Now().Add(5 * time.Second)
	for {
		if text := shellBufferText(sw); strings.Contains(text, "WMTV_PASTE_INS") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("clipboard paste never executed in bash; buffer:\n%s", shellBufferText(sw))
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestWMToggleCopyMode proves F5 (wm.ToggleCopyMode) toggles copy mode on the
// active shell window and is a no-op when the active window is a widget.
func TestWMToggleCopyMode(t *testing.T) {
	app := tview.NewApplication()
	wm := newWM(app, tview.NewBox())

	wm.ToggleCopyMode() // no windows: must not panic
	wm.openWidget()
	wm.ToggleCopyMode() // widget focused: must stay a no-op

	wm.openShellAt("/tmp")
	wm.nextWindow() // widget [1], shell [2]
	wm.nextWindow() // back to shell [1]
	if !wm.activeIsShell() {
		t.Fatal("expected the active window to be the shell")
	}
	if sw := wm.wins[wm.active].shell; sw.inCopyMode() {
		t.Fatal("copy mode must start off")
	}
	wm.ToggleCopyMode()
	if sw := wm.wins[wm.active].shell; !sw.inCopyMode() {
		t.Fatal("F5 must enter copy mode on the active shell")
	}
	wm.ToggleCopyMode()
	if sw := wm.wins[wm.active].shell; sw.inCopyMode() {
		t.Fatal("second F5 must leave copy mode")
	}
	wm.closeAll()
}

// stubWLPaste installs (PATH override, isolated tmpdir) a fake wl-paste that
// echoes the given text for `--primary`; used to prove Wayland-first ordering
// without touching system directories.
func stubWLPaste(t *testing.T, text string) {
	t.Helper()
	sandbox := t.TempDir()
	stub := filepath.Join(sandbox, "wl-paste")
	script := "#!/bin/sh\nif [ \"$1\" = \"--primary\" ]; then printf '%s' '" + text + "'; fi\n"
	if err := os.WriteFile(stub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", sandbox+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestClipboardOrderingBySession proves the clipboard tools are tried in the
// order of the active display server: wl-paste first on Wayland, xclip first
// on X11 — so a session is never slowed by the other family's tools.
func TestClipboardOrderingBySession(t *testing.T) {
	sandbox := stubXclip(t)          // xclip present
	stubWLPaste(t, "WAYLAND_MARKER") // wl-paste present too
	if err := os.WriteFile(filepath.Join(sandbox, "primary.txt"), []byte("X11_MARKER"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("WAYLAND_DISPLAY", "wayland-0")
	if got := primarySelection(); got != "WAYLAND_MARKER" {
		t.Fatalf("Wayland session: primarySelection = %q, want WAYLAND_MARKER (wl-paste must win over xclip)", got)
	}

	t.Setenv("WAYLAND_DISPLAY", "")
	if got := primarySelection(); got != "X11_MARKER" {
		t.Fatalf("X11 session: primarySelection = %q, want X11_MARKER (xclip must win over wl-paste)", got)
	}
}

// TestClipboardWriteOrderBySession proves setClipboard reaches wl-copy first on
// a Wayland session even when xclip is also installed (XWayland).
func TestClipboardWriteOrderBySession(t *testing.T) {
	t.Setenv("WAYLAND_DISPLAY", "wayland-0")
	sandbox := stubXclip(t) // xclip present

	// Simple wl-copy stub: writes its stdin to wl.txt and args to args.txt.
	wlStub := filepath.Join(sandbox, "wl-copy")
	script := "#!/bin/sh\necho \"$@\" > " + sandbox + "/wl_args.txt\ncat > " + sandbox + "/wl.txt\n"
	if err := os.WriteFile(wlStub, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", sandbox+string(os.PathListSeparator)+os.Getenv("PATH"))

	setClipboard("WMTV_CLIP_WAYLAND")

	tests := []struct{ file, want string }{
		{"wl.txt", "WMTV_CLIP_WAYLAND"},
		{"wl_args.txt", "--primary"},
	}
	deadline := time.Now().Add(3 * time.Second)
	for range 4 {
		ok := true
		for _, tc := range tests {
			b, err := os.ReadFile(filepath.Join(sandbox, tc.file))
			if err != nil || strings.TrimSpace(string(b)) != tc.want {
				ok = false
				break
			}
		}
		if ok {
			return
		}
		if time.Now().After(deadline) {
			got, _ := os.ReadFile(filepath.Join(sandbox, "wl_args.txt"))
			t.Fatalf("wl-copy not reached on Wayland first; wl_args.txt = %q", got)
		}
		time.Sleep(30 * time.Millisecond)
	}
}

// TestShellWindowPasteHandlerBlock proves the tview paste handler (terminal
// native Ctrl+Shift+V / right-click, bracketed paste) injects a whole
// multi-line block into a real bash, which executes every line: CRLF and LF
// are normalized to CR (Enter), so readline runs the lines one by one.
func TestShellWindowPasteHandlerBlock(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not found")
	}
	app := tview.NewApplication()
	sw, err := newShellWindow(app, "", nil, func() bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	defer sw.close()

	paste := sw.PasteHandler()
	paste("echo BLK_A\r\necho BLK_B\necho BLK_C\n", func(tview.Primitive) {})

	deadline := time.Now().Add(5 * time.Second)
	for {
		text := shellBufferText(sw)
		if strings.Contains(text, "BLK_A") && strings.Contains(text, "BLK_B") && strings.Contains(text, "BLK_C") {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("block paste never executed fully in bash; buffer:\n%s", text)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
