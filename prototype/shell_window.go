package main

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
	"github.com/gdamore/tcell/v2"
	"github.com/mattn/go-runewidth"
	"github.com/rivo/tview"
)

// shellWindow is a tview.Primitive embedding a live shell via creack/pty,
// rendered by the charmbracelet/x/vt VT emulator into its tview region.
//
// Concurrency: every access to the emulator's screen state (Write from the
// pty-read goroutine, Resize/Draw/Cursor* from the tview main thread) is
// serialized through w.mu. On top of that the emulator is a *vt.SafeEmulator,
// whose own mutex protects the screen state as a belt-and-suspenders.
type shellWindow struct {
	*tview.Box

	app         *tview.Application
	onExit      func()
	wantsRedraw func() bool // live: is this window's output worth a UI redraw?

	emu  *vt.SafeEmulator
	ptmx *os.File
	cmd  *exec.Cmd

	mu     sync.Mutex
	closed bool

	lastW, lastH int

	// Keyboard selection ("copy mode", F5), all guarded by w.mu. copyAnchor.X
	// == -1 while no selection start has been pinned with Space yet. selStart/
	// selEnd keep the last committed selection so Ctrl+Insert can re-copy it
	// after leaving copy mode.
	copyMode   bool
	copyPos    uv.Position // copy-mode cursor
	copyAnchor uv.Position // selection start; X==-1 when none
	selStart   uv.Position // last copied selection
	selEnd     uv.Position
}

func newShellWindow(app *tview.Application, dir string, onExit func(), wantsRedraw func() bool) (*shellWindow, error) {
	w := &shellWindow{
		Box:         tview.NewBox(),
		app:         app,
		onExit:      onExit,
		wantsRedraw: wantsRedraw,
		emu:         vt.NewSafeEmulator(80, 24),
	}
	// We render only the visible screen and have no in-window scroll UX, so keep
	// the VT scrollback tiny: the library default (10000 lines) balloons memory
	// when a window emits sustained output (yes/log floods). 200 lines bound
	// that to ~1MB per window while leaving room for future scroll support.
	w.emu.SetScrollbackSize(200)
	w.SetBorder(true).SetTitle(" shell ")

	cmd := exec.Command("/bin/bash")
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	ptmx, err := pty.Start(cmd)
	if err != nil {
		return nil, fmt.Errorf("pty start: %w", err)
	}
	w.cmd = cmd
	w.ptmx = ptmx

	// shell output -> emulator (with redraw only when this window is active),
	// emulator input/responses -> shell.
	go func() {
		buf := make([]byte, 4096)
		for {
			n, rerr := ptmx.Read(buf)
			if n > 0 {
				w.mu.Lock()
				if !w.closed {
					_, _ = w.emu.Write(buf[:n])
				}
				w.mu.Unlock()
				w.notifyOutput()
			}
			if rerr != nil {
				return
			}
		}
	}()
	go func() { _, _ = io.Copy(ptmx, w.emu) }()

	// Reap the child so it never becomes a zombie, and notify on natural exit.
	go func() {
		_ = cmd.Wait()
		w.mu.Lock()
		alreadyClosed := w.closed
		w.mu.Unlock()
		if !alreadyClosed && onExit != nil {
			onExit()
		}
	}()

	return w, nil
}

func (w *shellWindow) close() {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}
	w.closed = true
	w.mu.Unlock()

	if w.cmd != nil && w.cmd.Process != nil {
		_ = w.cmd.Process.Kill()
	}
	if w.ptmx != nil {
		_ = w.ptmx.Close()
	}
	// Unblock the emulator->pty copy goroutine by closing the emulator's input
	// pipe. We deliberately do NOT call emu.Close(): vt.SafeEmulator does not
	// guard Read()/Close(), and Close() would race with the copy goroutine's
	// Read() on the emulator's internal closed flag.
	if w.emu != nil {
		if pw, ok := w.emu.InputPipe().(*io.PipeWriter); ok {
			_ = pw.CloseWithError(io.EOF)
		}
	}
}

// shouldRedraw reports whether output from this shell warrants a full UI
// redraw. Only the active window (plus every window while the overview is up)
// redraws; idle background shells silently accumulate output into their VT
// buffer instead of hammering the main screen.
func (w *shellWindow) shouldRedraw() bool {
	if w.wantsRedraw == nil || !w.wantsRedraw() {
		return false
	}
	w.mu.Lock()
	closed := w.closed
	w.mu.Unlock()
	return !closed
}

// notifyOutput schedules a redraw iff this window warrants one.
func (w *shellWindow) notifyOutput() {
	if !w.shouldRedraw() {
		return
	}
	w.app.QueueUpdateDraw(func() {})
}

// altScreen reports whether the subprocess currently runs in the terminal's
// alternate screen — i.e. a full-screen TUI (top/htop/mc/vim/less) is in the
// foreground. On a plain bash prompt alt-screen is off.
func (w *shellWindow) altScreen() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return false
	}
	return w.emu.IsAltScreen()
}

// renderScaled paints a nearest-neighbour downscale of the emulator's buffer
// into area (a live thumbnail, e.g. inside the overview grid). The pty itself
// is untouched: only the active window's pty carries the real window size.
func (w *shellWindow) renderScaled(screen tcell.Screen, area image.Rectangle) {
	if area.Dx() <= 0 || area.Dy() <= 0 {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	ew, eh := w.emu.Width(), w.emu.Height()
	if ew <= 0 || eh <= 0 {
		return
	}
	for dy := 0; dy < area.Dy(); dy++ {
		sy := dy * eh / area.Dy()
		if sy >= eh {
			sy = eh - 1
		}
		for dx := 0; dx < area.Dx(); dx++ {
			sx := dx * ew / area.Dx()
			if sx >= ew {
				sx = ew - 1
			}
			c := w.emu.CellAt(sx, sy)
			if c == nil {
				continue
			}
			mainc := ' '
			var comb []rune
			if rs := []rune(c.Content); len(rs) > 0 {
				mainc = rs[0]
				comb = rs[1:]
			}
			screen.SetContent(area.Min.X+dx, area.Min.Y+dy, mainc, comb, toTcellStyle(c.Style))
		}
	}
}

// Cwd returns the shell process's current working directory (via /proc), so a
// saved session captures live `cd` changes. Empty if unavailable.
func (w *shellWindow) Cwd() string {
	if w.cmd == nil || w.cmd.Process == nil {
		return ""
	}
	c, err := os.Readlink(fmt.Sprintf("/proc/%d/cwd", w.cmd.Process.Pid))
	if err != nil {
		return ""
	}
	return c
}

func (w *shellWindow) Draw(screen tcell.Screen) {
	w.Box.DrawForSubclass(screen, w)

	x, y, width, height := w.GetInnerRect()
	if width <= 0 || height <= 0 {
		return
	}

	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return
	}

	if width != w.lastW || height != w.lastH {
		w.lastW, w.lastH = width, height
		w.emu.Resize(width, height)
		if w.ptmx != nil {
			_ = pty.Setsize(w.ptmx, &pty.Winsize{Rows: uint16(height), Cols: uint16(width)})
		}
	}

	area := image.Rect(x, y, x+width, y+height)
	w.emu.Draw(&uvAdapter{screen: screen}, area)

	cursorShown := !w.emu.CursorHidden()
	var cx, cy int
	if cursorShown {
		pos := w.emu.CursorPosition()
		cx, cy = x+pos.X, y+pos.Y
	}
	cm := w.copyMode
	copyAnchor := w.copyAnchor
	copyPos := w.copyPos
	w.mu.Unlock()

	// Keyboard selection mode draws its own highlight and cursor, and hides the
	// shell's real cursor for the duration.
	if cm {
		w.drawCopyOverlay(screen, x, y, width, height, copyAnchor, copyPos)
		px, py := x+copyPos.X, y+copyPos.Y
		if px < x+width && py < y+height {
			screen.ShowCursor(px, py)
		}
		return
	}

	if cursorShown && cx < x+width && cy < y+height {
		screen.ShowCursor(cx, cy)
	}
}

// drawCopyOverlay renders the copy-mode selection highlight (reverse video) and
// a one-line key hint at the top of the window.
func (w *shellWindow) drawCopyOverlay(screen tcell.Screen, ox, oy, ow, oh int, anchor, cpos uv.Position) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed || w.emu == nil {
		return
	}
	ew, eh := w.emu.Width(), w.emu.Height()
	if ew <= 0 || eh <= 0 {
		return
	}
	sel := anchor.X >= 0 && anchor.Y >= 0
	var a, b uv.Position
	if sel {
		a, b = normalizeSel(anchor, cpos, ew, eh)
	}
	for y := 0; y < eh; y++ {
		for x := 0; x < ew; x++ {
			if !sel || y < a.Y || y > b.Y || x < a.X || x > b.X {
				continue
			}
			c := w.emu.CellAt(x, y)
			if c == nil {
				continue
			}
			mainc := ' '
			var comb []rune
			if rs := []rune(c.Content); len(rs) > 0 {
				mainc = rs[0]
				comb = rs[1:]
			}
			screen.SetContent(ox+x, oy+y, mainc, comb, toTcellStyle(c.Style).Reverse(true))
		}
	}
	// A one-line hint at the top so the keyboard is discoverable.
	const hint = " COPY · стрелки/hjkl — курсор · Space — выделять · Enter — копировать · Esc/q/F5 — выход "
	runes := []rune(hint)
	st := tcell.StyleDefault.Background(tcell.ColorBlack).Foreground(tcell.ColorWhite).Bold(true)
	for x := 0; x < ew; x++ {
		r := rune(' ')
		if x < len(runes) {
			r = runes[x]
		}
		screen.SetContent(ox+x, oy, r, nil, st)
	}
}

func (w *shellWindow) InputHandler() func(*tcell.EventKey, func(tview.Primitive)) {
	return w.WrapInputHandler(func(event *tcell.EventKey, setFocus func(tview.Primitive)) {
		w.mu.Lock()
		closed := w.closed
		cm := w.copyMode
		w.mu.Unlock()
		if closed {
			return
		}

		// Copy/Paste shortcuts, live outside copy mode too:
		// Ctrl+Insert copies the last selection, Shift+Insert pastes the clipboard.
		if event.Key() == tcell.KeyInsert {
			switch {
			case event.Modifiers()&tcell.ModCtrl != 0:
				w.copySelection()
				return
			case event.Modifiers()&tcell.ModShift != 0:
				w.pasteClipboard()
				return
			}
		}

		if cm {
			w.handleCopyModeKey(event)
			return
		}

		b := keyBytes(event)
		if len(b) == 0 {
			return
		}
		_, _ = w.emu.InputPipe().Write(b)
	})
}

// MouseHandler intercepts the middle mouse button and pastes the X11/Wayland
// PRIMARY selection (the terminal highlight buffer) into the shell. While this
// app's mouse tracking is enabled, terminals suppress their native middle-click
// paste and hand the button event to the app instead — so we read the selection
// via a clipboard tool and inject it into the pty. All other actions keep the
// Box default (left-click focus).
func (w *shellWindow) MouseHandler() func(tview.MouseAction, *tcell.EventMouse, func(tview.Primitive)) (bool, tview.Primitive) {
	boxDefault := w.Box.MouseHandler()
	return w.Box.WrapMouseHandler(func(action tview.MouseAction, event *tcell.EventMouse, setFocus func(tview.Primitive)) (bool, tview.Primitive) {
		if action != tview.MouseMiddleClick && action != tview.MouseMiddleDown {
			return boxDefault(action, event, setFocus)
		}
		if !w.InRect(event.Position()) {
			return false, nil
		}
		// Reading the selection talks to the display server and can block (a
		// stalled compositor, an unset X selection), so never run it on the
		// event loop: inject asynchronously once the text arrives.
		go w.pastePrimary()
		return true, nil
	})
}

// pastePrimary reads the PRIMARY selection from the display server and writes
// it into the shell's pty (no-op if nothing is selected or no clipboard tool
// is available).
func (w *shellWindow) pastePrimary() {
	text := primarySelection()
	if text == "" {
		return
	}
	w.writeTextToPty(text)
}

// normalizePasteNewlines renders pasted newlines the way terminals do when
// injecting a selection into a tty: each line break becomes CR (Enter), so
// readline executes the pasted command lines one by one.
func normalizePasteNewlines(s string) string {
	if !strings.ContainsAny(s, "\r\n") {
		return s
	}
	return strings.NewReplacer("\r\n", "\r", "\n", "\r").Replace(s)
}

// waylandSession reports whether we run under a Wayland display server. The
// clipboard tools are ordered by session so the right family is tried first:
// on Wayland, xclip/xsel talk to XWayland and either fail or hang (up to the
// tool timeout), so wl-paste/wl-copy go first; on X11 the reverse holds.
func waylandSession() bool {
	return os.Getenv("WAYLAND_DISPLAY") != ""
}

// clipboardReadTools returns the read tools in display-server order.
func clipboardReadTools(primary bool) [][]string {
	var x11, wl [][]string
	if primary {
		x11 = [][]string{{"xclip", "-o", "-selection", "primary"}, {"xsel", "-o", "--primary"}}
		wl = [][]string{{"wl-paste", "--primary"}}
	} else {
		x11 = [][]string{{"xclip", "-o", "-selection", "clipboard"}, {"xsel", "-o", "-b"}}
		wl = [][]string{{"wl-paste"}}
	}
	if waylandSession() {
		return append(wl, x11...)
	}
	return append(x11, wl...)
}

// clipboardWriteTools returns the write tools in display-server order; both
// the CLIPBOARD and PRIMARY selections are served so middle-click paste works.
func clipboardWriteTools() [][]string {
	x11 := [][]string{
		{"xclip", "-selection", "clipboard"},
		{"xclip", "-selection", "primary"},
		{"xsel", "-b"},
		{"xsel", "-p"},
	}
	wl := [][]string{{"wl-copy"}, {"wl-copy", "--primary"}}
	if waylandSession() {
		return append(wl, x11...)
	}
	return append(x11, wl...)
}

// primarySelection returns the display server's PRIMARY selection text, or ""
// if there is none or no clipboard tool is available.
func primarySelection() string {
	for _, t := range clipboardReadTools(true) {
		if out, ok := runSelectionTool(t[0], t[1:]...); ok && out != "" {
			return out
		}
	}
	return ""
}

// runSelectionTool runs a clipboard tool with a short timeout; the second
// return value reports whether the tool exists and produced output.
func runSelectionTool(name string, args ...string) (string, bool) {
	path, err := exec.LookPath(name)
	if err != nil {
		return "", false
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, args...).Output()
	if err != nil {
		return "", false
	}
	return string(out), true
}

// clipboardSelection returns the CLIPBOARD selection (Ctrl+Insert copy
// target), falling back to PRIMARY (xterm's Shift+Insert pastes primary).
func clipboardSelection() string {
	for _, t := range clipboardReadTools(false) {
		if out, ok := runSelectionTool(t[0], t[1:]...); ok && out != "" {
			return out
		}
	}
	return primarySelection()
}

// setClipboard writes text to BOTH the X11/Wayland CLIPBOARD and PRIMARY
// selections (the latter keeps middle-button paste working). Feeds each tool's
// stdin, so any available tool works; failures are ignored (async context).
func setClipboard(text string) {
	if text == "" {
		return
	}
	for _, args := range clipboardWriteTools() {
		writeClipboard(args, text)
	}
}

// writeClipboard runs a clipboard tool with text on stdin; it never blocks the
// caller for more than the 500ms tool timeout.
func writeClipboard(args []string, text string) {
	if len(args) == 0 {
		return
	}
	path, err := exec.LookPath(args[0])
	if err != nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, args[1:]...)
	cmd.Stdin = strings.NewReader(text)
	_ = cmd.Run()
}

// --- Keyboard selection (copy mode) -----------------------------------------

// ToggleCopyMode enters or leaves keyboard selection mode. On entry the cursor
// starts at the shell cursor position and the previous selection is cleared.
func (w *shellWindow) ToggleCopyMode() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return
	}
	w.copyMode = !w.copyMode
	if w.copyMode {
		pos := uv.Position{X: -1, Y: -1}
		if w.emu != nil {
			pos = w.emu.CursorPosition()
		}
		w.copyPos = pos
		w.copyAnchor = uv.Position{X: -1, Y: -1}
	}
}

// inCopyMode reports whether keyboard selection mode is active.
func (w *shellWindow) inCopyMode() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.copyMode
}

// pasteClipboard reads the CLIPBOARD (Shift+Insert) and injects it into the
// pty, async (display server access can block).
func (w *shellWindow) pasteClipboard() {
	go func() {
		text := clipboardSelection()
		if text == "" {
			return
		}
		w.writeTextToPty(text)
	}()
}

// copySelection copies the last committed selection to the clipboard
// (Ctrl+Insert). No-op if nothing has been selected yet.
func (w *shellWindow) copySelection() {
	w.mu.Lock()
	text := w.selectedTextLocked()
	w.mu.Unlock()
	if text == "" {
		return
	}
	go setClipboard(text)
}

// handleCopyModeKey processes a key event while copy mode is active. Navigation
// (arrows + hjkl), Space pins the selection start, Enter copies and exits, F5/
// Esc/q exit without copying.
func (w *shellWindow) handleCopyModeKey(event *tcell.EventKey) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed || w.emu == nil {
		return
	}
	ew, eh := w.emu.Width(), w.emu.Height()
	if ew <= 0 || eh <= 0 {
		return
	}
	switch {
	case isOvNavKey(event, tcell.KeyUp, 'k'):
		w.moveCopyCursor(0, -1, ew, eh)
	case isOvNavKey(event, tcell.KeyDown, 'j'):
		w.moveCopyCursor(0, 1, ew, eh)
	case isOvNavKey(event, tcell.KeyLeft, 'h'):
		w.moveCopyCursor(-1, 0, ew, eh)
	case isOvNavKey(event, tcell.KeyRight, 'l'):
		w.moveCopyCursor(1, 0, ew, eh)
	case event.Key() == tcell.KeyPgUp:
		w.moveCopyCursor(0, -(eh/2 + 1), ew, eh)
	case event.Key() == tcell.KeyPgDn:
		w.moveCopyCursor(0, eh/2+1, ew, eh)
	case event.Key() == tcell.KeyHome:
		w.copyPos = uv.Position{X: 0, Y: w.copyPos.Y}
	case event.Key() == tcell.KeyEnd:
		w.copyPos = uv.Position{X: ew - 1, Y: w.copyPos.Y}
	case event.Key() == tcell.KeyRune && event.Rune() == ' ':
		w.copyAnchor = w.copyPos
	case event.Key() == tcell.KeyEnter:
		w.commitSelection(ew, eh)
	case event.Key() == tcell.KeyEscape || event.Key() == tcell.KeyF5 || isOvNavKey(event, tcell.KeyRune, 'q'):
		w.copyMode = false
	}
}

// moveCopyCursor moves the copy-mode cursor with clamping to the emulator grid.
func (w *shellWindow) moveCopyCursor(dx, dy, ew, eh int) {
	p := w.copyPos
	if p.X < 0 || p.Y < 0 {
		p = uv.Position{}
	}
	p.X += dx
	p.Y += dy
	if p.X < 0 {
		p.X = 0
	}
	if p.X >= ew {
		p.X = ew - 1
	}
	if p.Y < 0 {
		p.Y = 0
	}
	if p.Y >= eh {
		p.Y = eh - 1
	}
	w.copyPos = p
}

// commitSelection pins the current anchor..cursor rectangle as the selection,
// copies it to the clipboard and exits copy mode.
func (w *shellWindow) commitSelection(ew, eh int) {
	anchor := w.copyAnchor
	cpos := w.copyPos
	w.copyMode = false
	if anchor.X < 0 || anchor.Y < 0 {
		return
	}
	w.selStart, w.selEnd = normalizeSel(anchor, cpos, ew, eh)
	text := w.selectedTextLocked()
	if text != "" {
		go setClipboard(text)
	}
}

// normalizeSel orders and bounds a selection rectangle.
func normalizeSel(a, b uv.Position, ew, eh int) (uv.Position, uv.Position) {
	if a.Y > b.Y || (a.Y == b.Y && a.X > b.X) {
		a, b = b, a
	}
	if a.Y < 0 {
		a.Y = 0
	}
	if a.X < 0 {
		a.X = 0
	}
	if b.Y >= eh {
		b.Y = eh - 1
	}
	if b.X >= ew {
		b.X = ew - 1
	}
	return a, b
}

// selectedTextLocked assembles the committed selection (selStart..selEnd) into
// text, trimming trailing blanks per line and joining lines with \n. Must be
// called with w.mu held.
func (w *shellWindow) selectedTextLocked() string {
	if w.closed || w.emu == nil {
		return ""
	}
	a, b := w.selStart, w.selEnd
	if a.Y < 0 || b.Y < 0 {
		return ""
	}
	ew, eh := w.emu.Width(), w.emu.Height()
	a, b = normalizeSel(a, b, ew, eh)
	var sb strings.Builder
	for y := a.Y; y <= b.Y; y++ {
		start, end := 0, ew-1
		if y == a.Y {
			start = a.X
		}
		if y == b.Y {
			end = b.X
		}
		var line strings.Builder
		for x := start; x <= end; x++ {
			if c := w.emu.CellAt(x, y); c != nil && c.Content != "" {
				line.WriteString(c.Content)
			}
		}
		sb.WriteString(strings.TrimRight(line.String(), " \t"))
		if y != b.Y {
			sb.WriteByte('\n')
		}
	}
	return sb.String()
}

// writeTextToPty pipes text into the shell's pty (normalized newlines). The
// whole block goes out in one write via emu.Paste, which also wraps it in
// bracketed-paste escapes (\x1b[200~…\x1b[201~) whenever the program inside
// has enabled that mode — so readline/vim treat it as a single paste instead
// of per-character input.
func (w *shellWindow) writeTextToPty(text string) {
	if text == "" {
		return
	}
	w.mu.Lock()
	closed := w.closed
	var emu *vt.SafeEmulator
	if !closed {
		emu = w.emu
	}
	w.mu.Unlock()
	if closed || emu == nil {
		return
	}
	emu.Paste(normalizePasteNewlines(text))
}

// PasteHandler receives terminal-native paste (Ctrl+Shift+V / right-click,
// bracketed paste) as one block and injects it into the shell the same way as
// Shift+Insert / middle-click. tview calls this only while pasting is enabled
// (see Application.EnablePaste); without it the text would otherwise arrive as
// a char-by-char key stream.
func (w *shellWindow) PasteHandler() func(string, func(tview.Primitive)) {
	return w.WrapPasteHandler(func(text string, _ func(tview.Primitive)) {
		w.writeTextToPty(text)
	})
}

// keyBytes translates a tcell key event into the byte sequence a shell expects.
func keyBytes(e *tcell.EventKey) []byte {
	switch e.Key() {
	case tcell.KeyRune:
		b := make([]byte, 0, 4)
		if e.Modifiers()&tcell.ModAlt != 0 {
			b = append(b, 0x1b)
		}
		return utf8.AppendRune(b, e.Rune())
	case tcell.KeyEnter:
		return []byte{'\r'}
	case tcell.KeyTab:
		return []byte{'\t'}
	case tcell.KeyBacktab:
		return []byte("\x1b[Z")
	case tcell.KeyBackspace, tcell.KeyBackspace2:
		return []byte{0x7f}
	case tcell.KeyEscape:
		return []byte{0x1b}
	case tcell.KeyUp:
		return []byte("\x1b[A")
	case tcell.KeyDown:
		return []byte("\x1b[B")
	case tcell.KeyRight:
		return []byte("\x1b[C")
	case tcell.KeyLeft:
		return []byte("\x1b[D")
	case tcell.KeyHome:
		return []byte("\x1b[H")
	case tcell.KeyEnd:
		return []byte("\x1b[F")
	case tcell.KeyInsert:
		return []byte("\x1b[2~")
	case tcell.KeyDelete:
		return []byte("\x1b[3~")
	case tcell.KeyPgUp:
		return []byte("\x1b[5~")
	case tcell.KeyPgDn:
		return []byte("\x1b[6~")
	case tcell.KeyF1:
		return []byte("\x1bOP")
	case tcell.KeyF2:
		return []byte("\x1bOQ")
	case tcell.KeyF3:
		return []byte("\x1bOR")
	case tcell.KeyF4:
		return []byte("\x1bOS")
	case tcell.KeyF5:
		return []byte("\x1b[15~")
	case tcell.KeyF6:
		return []byte("\x1b[17~")
	case tcell.KeyF7:
		return []byte("\x1b[18~")
	case tcell.KeyF8:
		return []byte("\x1b[19~")
	case tcell.KeyF9:
		return []byte("\x1b[20~")
	case tcell.KeyF10:
		return []byte("\x1b[21~")
	case tcell.KeyF11:
		return []byte("\x1b[23~")
	case tcell.KeyF12:
		return []byte("\x1b[24~")
	}
	if e.Key() >= tcell.KeyCtrlA && e.Key() <= tcell.KeyCtrlZ {
		return []byte{byte(e.Key() - tcell.KeyCtrlA + 1)}
	}
	return nil
}

// uvAdapter implements uv.Screen on top of a tcell.Screen.
type uvAdapter struct {
	screen tcell.Screen
}

func (a *uvAdapter) Bounds() uv.Rectangle {
	w, h := a.screen.Size()
	return image.Rect(0, 0, w, h)
}

func (a *uvAdapter) CellAt(x, y int) *uv.Cell {
	return nil
}

func (a *uvAdapter) SetCell(x, y int, c *uv.Cell) {
	if c == nil {
		return
	}
	mainc := ' '
	var comb []rune
	if rs := []rune(c.Content); len(rs) > 0 {
		mainc = rs[0]
		comb = rs[1:]
	}
	a.screen.SetContent(x, y, mainc, comb, toTcellStyle(c.Style))
}

func (a *uvAdapter) WidthMethod() uv.WidthMethod {
	return widthMethod{}
}

type widthMethod struct{}

func (widthMethod) StringWidth(s string) int {
	return runewidth.StringWidth(s)
}

func toTcellStyle(s uv.Style) tcell.Style {
	st := tcell.StyleDefault
	if s.Fg != nil {
		st = st.Foreground(toTcellColor(s.Fg))
	}
	if s.Bg != nil {
		st = st.Background(toTcellColor(s.Bg))
	}
	if s.Attrs&uv.AttrBold != 0 {
		st = st.Bold(true)
	}
	if s.Attrs&uv.AttrItalic != 0 {
		st = st.Italic(true)
	}
	if s.Attrs&uv.AttrBlink != 0 {
		st = st.Blink(true)
	}
	if s.Attrs&uv.AttrReverse != 0 {
		st = st.Reverse(true)
	}
	if s.Attrs&uv.AttrStrikethrough != 0 {
		st = st.StrikeThrough(true)
	}
	if s.Underline != uv.UnderlineStyleNone {
		st = st.Underline(true)
	}
	return st
}

func toTcellColor(c color.Color) tcell.Color {
	r, g, b, _ := c.RGBA()
	return tcell.NewRGBColor(int32(r>>8), int32(g>>8), int32(b>>8))
}
