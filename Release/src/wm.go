package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// WM is a minimal window manager: a base UI behind, a list of windows on top.
// Only the active window is drawn and receives input; inactive windows stay
// buffered (their VT emulators keep consuming pty output on their own).
type WM struct {
	*tview.Box
	app    *tview.Application
	base   tview.Primitive
	nextID int

	mu          sync.Mutex
	wins        []*Window
	active      int
	ovf         *overview // non-nil while the F9 overview is open (transient)
	fKeyForceWM bool      // F12-toggle: plain F-keys are always WM commands
}

// Window is a single managed window. content is either a *shellWindow or an
// arbitrary tview widget. shell is non-nil iff content is a *shellWindow.
type Window struct {
	id      int
	title   string
	content tview.Primitive
	shell   *shellWindow
}

type titler interface {
	SetTitle(string) *tview.Box
}

func newWM(app *tview.Application, base tview.Primitive) *WM {
	return &WM{Box: tview.NewBox(), app: app, base: base}
}

func (wm *WM) hasWindows() bool {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	return len(wm.wins) > 0
}

func (wm *WM) addWindow(title string, content tview.Primitive, shell *shellWindow) {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	wm.wins = append(wm.wins, &Window{
		id:      wm.nextID,
		title:   title,
		content: content,
		shell:   shell,
	})
	wm.nextID++
	wm.active = len(wm.wins) - 1
}

// openShell starts a new shell in its own window.
func (wm *WM) openShell() {
	wm.openShellAt("")
}

// openShellAt starts a new shell window in the given directory ("" = inherit).
func (wm *WM) openShellAt(dir string) {
	wm.openPTYWindowAt("shell", []string{"/bin/bash"}, dir)
}

// openProgramAt starts a program (restored foreground program) in its own
// window at dir. It returns false when the program cannot be started, so the
// caller can fall back to a plain shell.
func (wm *WM) openProgramAt(argv []string, dir string) bool {
	if len(argv) == 0 {
		return false
	}
	if _, err := exec.LookPath(argv[0]); err != nil {
		return false
	}
	return wm.openPTYWindowAt(programTitle(argv), argv, dir)
}

// openPTYWindowAt is the shared constructor for shell and program windows.
func (wm *WM) openPTYWindowAt(title string, argv []string, dir string) bool {
	if dir != "" {
		if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
			dir = ""
		}
	}
	var sw *shellWindow
	w, err := newPTYWindow(wm.app, argv, dir, func() {
		wm.app.QueueUpdateDraw(func() { wm.removeByShell(sw) })
	}, func() bool {
		return wm.isActiveWindow(sw) || wm.overviewOpen()
	})
	if err != nil {
		return false
	}
	sw = w
	wm.addWindow(title, w, w)
	return true
}

// isActiveWindow reports whether the given content is the active window.
func (wm *WM) isActiveWindow(content tview.Primitive) bool {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	if len(wm.wins) == 0 {
		return false
	}
	return wm.wins[wm.active].content == content
}

// activeIsShell reports whether the focused window is a pty shell.
func (wm *WM) activeIsShell() bool {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	if len(wm.wins) == 0 {
		return false
	}
	return wm.wins[wm.active].shell != nil
}

// activeShellAltScreen reports whether the focused shell window is currently
// running a full-screen TUI (its VT emulator is in alternate-screen mode).
// Used to decide whether F-keys belong to the program inside or to the WM.
func (wm *WM) activeShellAltScreen() bool {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	if len(wm.wins) == 0 {
		return false
	}
	w := wm.wins[wm.active]
	if w.shell == nil {
		return false
	}
	return w.shell.altScreen()
}

// ToggleFKeyMode flips the F12 mode: auto (contextual by alt-screen) <-> wm
// (plain F-keys are always WM commands, even inside a full-screen TUI).
func (wm *WM) ToggleFKeyMode() {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	wm.fKeyForceWM = !wm.fKeyForceWM
}

func (wm *WM) fKeyForceWMRead() bool {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	return wm.fKeyForceWM
}

// FKeyModeLabel returns the short mode tag shown in window titles.
func (wm *WM) FKeyModeLabel() string {
	if wm.fKeyForceWMRead() {
		return "F12: WM"
	}
	return "F12: auto"
}

// shouldPassFKey reports whether a plain F-key must be handed to the focused
// shell instead of being consumed as a WM command. F12 is always the WM mode
// switcher. In forced-WM mode nothing passes through; in auto mode F-keys pass
// through only while a full-screen TUI (alt-screen) is running inside.
func (wm *WM) shouldPassFKey(e *tcell.EventKey) bool {
	if e.Key() == tcell.KeyF12 {
		return false
	}
	if wm.fKeyForceWMRead() {
		return false
	}
	return wmFKeyPassThrough(e, wm.activeShellAltScreen())
}

// ToggleCopyMode enters/leaves the keyboard selection (copy) mode on the
// focused shell window (F5).
func (wm *WM) ToggleCopyMode() {
	wm.mu.Lock()
	var sw *shellWindow
	if len(wm.wins) > 0 {
		sw = wm.wins[wm.active].shell
	}
	wm.mu.Unlock()
	if sw != nil {
		sw.ToggleCopyMode()
	}
}

// openWidget opens a demo tview-widget window (any tview.Primitive would do).
func (wm *WM) openWidget() {
	v := tview.NewTextView().
		SetText("Это окно-виджет (tview.Primitive, не pty).\nЗдесь может быть любой tview-виджет:\nTable, Tree, Form, List...\n\nF3 — следующее окно · F4 — закрыть окно")
	v.SetBorder(true)
	v.SetDynamicColors(true)
	wm.addWindow("widget", v, nil)
}

// nextWindow cycles focus to the next window.
func (wm *WM) nextWindow() {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	if len(wm.wins) == 0 {
		return
	}
	wm.active = (wm.active + 1) % len(wm.wins)
}

// closeActive closes the active window, killing its shell if any.
func (wm *WM) closeActive() {
	wm.mu.Lock()
	wm.closeLocked(wm.active, true)
	wm.mu.Unlock()
}

// removeByShell removes the window that owns the given shell window (used when
// the shell process exits on its own, e.g. Ctrl-D).
func (wm *WM) removeByShell(sw *shellWindow) {
	if sw == nil {
		return
	}
	wm.mu.Lock()
	idx := -1
	for i, w := range wm.wins {
		if w.shell == sw {
			idx = i
			break
		}
	}
	wm.closeLocked(idx, false)
	wm.mu.Unlock()
}

// closeLocked removes window at idx. It must be called with wm.mu held.
func (wm *WM) closeLocked(idx int, killContent bool) {
	if idx < 0 || idx >= len(wm.wins) {
		return
	}
	w := wm.wins[idx]
	wm.wins = append(wm.wins[:idx], wm.wins[idx+1:]...)
	if len(wm.wins) == 0 {
		wm.active = 0
	} else if wm.active >= len(wm.wins) {
		wm.active = len(wm.wins) - 1
	} else if wm.active > idx {
		wm.active--
	}
	if killContent && w.shell != nil {
		w.shell.close()
	}
}

// snapshot captures the current workspace as session slots. A shell window
// records the program currently in its foreground (afm/micro/vim/...) with its
// argv and live cwd; at a plain prompt it records `shell` + the live cwd of
// the shell (or of a nested shell). Widgets record only their name.
func (wm *WM) snapshot() []SessionWindow {
	wm.mu.Lock()
	wins := make([]*Window, len(wm.wins))
	copy(wins, wm.wins)
	wm.mu.Unlock()

	out := make([]SessionWindow, 0, len(wins))
	for _, w := range wins {
		if w.shell != nil {
			argv, dir, atPrompt := w.shell.Foreground()
			if dir == "" {
				dir = w.shell.Cwd()
			}
			if atPrompt || len(argv) == 0 {
				out = append(out, SessionWindow{
					Kind: kindShell,
					Argv: []string{"/bin/bash"},
					Path: dir,
					Name: w.title,
				})
				continue
			}
			out = append(out, SessionWindow{
				Kind: kindProg,
				Argv: argv,
				Path: dir,
				Name: programTitle(argv),
			})
			continue
		}
		out = append(out, SessionWindow{Kind: kindWidget, Name: w.title})
	}
	return out
}

// restore replaces the current workspace with the given session slots. It
// returns the number of program slots that had to fall back to a plain shell
// (the program binary is gone or could not be started).
func (wm *WM) restore(wins []SessionWindow) (fallbacks int) {
	wm.closeAll()
	for _, s := range wins {
		switch s.Kind {
		case kindShell:
			wm.openShellAt(s.Path)
		case kindProg:
			if !wm.openProgramAt(restoreArgv(s), s.Path) {
				wm.openShellAt(s.Path)
				fallbacks++
			}
		case kindWidget:
			wm.openWidget()
		}
	}
	return fallbacks
}

// restoreArgv returns the argv to launch for a program slot. afm ignores its
// working directory and starts in $HOME unless a directory is passed as the
// first non-flag argument, so for afm the saved path is appended (best-effort:
// the exact pane directory inside afm is not knowable from outside).
func restoreArgv(s SessionWindow) []string {
	argv := append([]string(nil), s.Argv...)
	if len(argv) == 0 {
		return argv
	}
	name := strings.ToLower(filepath.Base(argv[0]))
	if name != "afm" && !strings.HasPrefix(name, "afm_linux") {
		return argv
	}
	if argvHasPathArg(argv) || s.Path == "" {
		return argv
	}
	if fi, err := os.Stat(s.Path); err != nil || !fi.IsDir() {
		return argv
	}
	return append(argv, s.Path)
}

// argvHasPathArg reports whether argv already carries a positional (non-flag)
// argument, e.g. `afm /tmp/x` or `micro file.txt`.
func argvHasPathArg(argv []string) bool {
	for _, a := range argv[1:] {
		if !strings.HasPrefix(a, "-") {
			return true
		}
	}
	return false
}

// closeAll kills every shell window (used on application exit / restore).
func (wm *WM) closeAll() {
	wm.mu.Lock()
	for _, w := range wm.wins {
		if w.shell != nil {
			w.shell.close()
		}
	}
	wm.wins = nil
	wm.active = 0
	wm.ovf = nil
	wm.mu.Unlock()
}

// --- Overview (modal F9 window switcher) ----------------------------------

// OpenOverview opens the transient overview; no-op with zero windows.
func (wm *WM) OpenOverview() {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	if len(wm.wins) == 0 || wm.ovf != nil {
		return
	}
	wm.ovf = newOverview(wm)
}

// CloseOverview closes the transient overview without changing the selection.
func (wm *WM) CloseOverview() {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	wm.ovf = nil
}

// overviewOpen reports whether the overview is currently shown.
func (wm *WM) overviewOpen() bool {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	return wm.ovf != nil
}

// activateOverview makes window idx active and closes the overview.
func (wm *WM) activateOverview(idx int) {
	wm.mu.Lock()
	defer wm.mu.Unlock()
	if idx >= 0 && idx < len(wm.wins) {
		wm.active = idx
	}
	wm.ovf = nil
}

// --- tview.Primitive implementation ----------------------------------------

func (wm *WM) Draw(screen tcell.Screen) {
	x, y, w, h := wm.GetRect()
	wm.base.SetRect(x, y, w, h)
	wm.base.Draw(screen)

	wm.mu.Lock()
	n := len(wm.wins)
	act := wm.active
	var content tview.Primitive
	var title string
	if n > 0 {
		content = wm.wins[act].content
		title = wm.wins[act].title
	}
	ovf := wm.ovf
	wm.mu.Unlock()
	if n == 0 {
		if ovf != nil {
			ovf.SetRect(x, y, w, h)
			ovf.Draw(screen)
		}
		return
	}

	// Max geometry: the window fills almost the whole area, leaving a 1-cell
	// margin to the thin tview.Box border. Clamp to a single cell so a
	// degenerate tiny resize never panics.
	mw, mh := w-2, h-2
	if mw < 1 {
		mw = 1
	}
	if mh < 1 {
		mh = 1
	}
	mx, my := x+1, y+1
	if t, ok := content.(titler); ok {
		tag := wm.FKeyModeLabel()
		if sw, ok := content.(*shellWindow); ok && sw.inCopyMode() {
			tag = "COPY · " + tag
		}
		t.SetTitle(fmt.Sprintf(" %s [%d/%d] · F3 next · F4 close · %s ", title, act+1, n, tag))
	}
	content.SetRect(mx, my, mw, mh)
	content.Draw(screen)

	// Modal overview on top of everything while it is open.
	if ovf != nil {
		ow, oh := w*9/10, h*9/10
		ox, oy := x+(w-ow)/2, y+(h-oh)/2
		ovf.SetRect(ox, oy, ow, oh)
		ovf.Draw(screen)
		screen.HideCursor()
	}
}

func (wm *WM) Focus(delegate func(p tview.Primitive)) {
	wm.mu.Lock()
	n := len(wm.wins)
	var content tview.Primitive
	if n > 0 {
		content = wm.wins[wm.active].content
	}
	wm.mu.Unlock()
	if content != nil {
		content.Focus(delegate)
		return
	}
	wm.Box.Focus(delegate)
}

func (wm *WM) HasFocus() bool {
	wm.mu.Lock()
	n := len(wm.wins)
	wm.mu.Unlock()
	if n > 0 {
		return true
	}
	return wm.Box.HasFocus()
}

func (wm *WM) InputHandler() func(*tcell.EventKey, func(tview.Primitive)) {
	wm.mu.Lock()
	ovf := wm.ovf
	var content tview.Primitive
	if ovf == nil && len(wm.wins) > 0 {
		content = wm.wins[wm.active].content
	}
	wm.mu.Unlock()
	if ovf != nil {
		return ovf.InputHandler()
	}
	if content != nil {
		return content.InputHandler()
	}
	return wm.Box.InputHandler()
}

func (wm *WM) MouseHandler() func(tview.MouseAction, *tcell.EventMouse, func(tview.Primitive)) (bool, tview.Primitive) {
	wm.mu.Lock()
	ovf := wm.ovf
	var content tview.Primitive
	if ovf == nil && len(wm.wins) > 0 {
		content = wm.wins[wm.active].content
	}
	wm.mu.Unlock()
	if ovf != nil {
		return ovf.MouseHandler()
	}
	if content != nil {
		return content.MouseHandler()
	}
	return wm.Box.MouseHandler()
}

func (wm *WM) PasteHandler() func(string, func(tview.Primitive)) {
	wm.mu.Lock()
	var content tview.Primitive
	if len(wm.wins) > 0 {
		content = wm.wins[wm.active].content
	}
	wm.mu.Unlock()
	if content != nil {
		return content.PasteHandler()
	}
	return wm.Box.PasteHandler()
}
