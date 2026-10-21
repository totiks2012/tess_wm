package main

import (
	"fmt"
	"os"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

const baseText = "Основное приложение.\n\n" +
	"F2 — новое окно shell · F3 — следующее окно · F4 — закрыть окно · F6 — окно-виджет\n" +
	"F5 — выбор/копирование мышью-с-клавиатуры (copy mode): стрелки/hjkl — курсор (включая историю), Space — выделять, Enter — копировать, Esc/q/F5 — выход\n" +
	"ЛКМ-драг по тексту — выделить (история тоже), отпустить — в буфер; ЛКМ-клик — снять выделение\n" +
	"PgUp/PgDn (Shift — тоже), Alt+k/j, колесо мыши (GUI) — прокрутка истории shell-окна\n" +
	"Ctrl+Insert — копировать выделенное (после copy mode) · Shift+Insert — вставить из буфера · средняя кнопка мыши — вставить PRIMARY\n" +
	"F7 — сохранить сессию · F8 — восстановить сессию · F9 — обзор окон (клик по миниатюре — вход)\n" +
	"F12 — режим F-клавиш: auto (в полноэкранной программе F-клавиши уходят внутрь) / WM (всегда команды WM); Alt+Fn — всегда команды WM"

// wmFKeyPassThrough reports whether an F-key must be handed to the focused
// shell instead of being consumed as a WM command. This happens only while a
// full-screen TUI (top/htop/mc/vim/less) is running inside the window — its
// alt-screen state is on. At a plain prompt or during ordinary output the
// F-keys belong to the WM. Alt+Fn is always the WM escape hatch.
func wmFKeyPassThrough(e *tcell.EventKey, shellInFullscreen bool) bool {
	return shellInFullscreen && e.Modifiers()&tcell.ModAlt == 0 &&
		e.Key() >= tcell.KeyF1 && e.Key() <= tcell.KeyF12
}

func main() {
	app := tview.NewApplication()
	// Terminal-native paste (Ctrl+Shift+V / right-click) arrives as bracketed
	// paste. With paste enabled tview collects it into one block and hands it to
	// the focused primitive's PasteHandler instead of a char-by-char key stream.
	app.EnablePaste(true)

	// Mouse only on a GUI terminal: in a pure Linux TTY the mouse would produce
	// garbage escape sequences and has no place (scrollback is reached via
	// Shift+PgUp / Alt+k). The detection is deterministic (env + /proc fd0);
	// tcell's EnableMouse is additionally a no-op when the TERM has no mouse
	// capability, so this is belt-and-suspenders.
	if isGUITerminal() && termSupportsMouse() {
		app.EnableMouse(true)
	}

	base := tview.NewTextView().SetText(baseText)
	base.SetBorder(true).SetTitle(" tess_wm (scrollback-шлифовка 2026-10) ")

	setStatus := func(s string) {
		base.SetText(baseText + "\n\n" + s)
	}

	wm := newWM(app, base)
	app.SetRoot(wm, true)

	// Auto-restore the saved session on start (so `me wm` raises the last workspace).
	if wins, err := loadSession(sessionFilePath()); err == nil && len(wins) > 0 {
		wm.restore(wins)
		setStatus(fmt.Sprintf("restored: %d window(s) from %s", len(wins), sessionFilePath()))
	}

	app.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		if event.Key() >= tcell.KeyF1 && event.Key() <= tcell.KeyF12 {
			// F12 toggles the F-key policy and is always handled by the WM
			// (even inside a full-screen program).
			if event.Key() == tcell.KeyF12 {
				wm.ToggleFKeyMode()
				return nil
			}
			// In auto mode a full-screen TUI inside the focused shell
			// (top/htop/mc/vim/less, detected via alt-screen) owns its
			// F-keys; WM commands are reached with Alt+Fn. In forced-WM
			// mode (F12) plain F-keys are always WM commands.
			if wm.shouldPassFKey(event) {
				return event
			}
			switch event.Key() {
			case tcell.KeyF2:
				wm.openShell()
				return nil
			case tcell.KeyF3:
				wm.nextWindow()
				return nil
			case tcell.KeyF5:
				wm.ToggleCopyMode()
				return nil
			case tcell.KeyF4:
				wm.closeActive()
				return nil
			case tcell.KeyF6:
				wm.openWidget()
				return nil
			case tcell.KeyF7:
				wins := wm.snapshot()
				if err := saveSession(sessionFilePath(), wins); err != nil {
					setStatus("save error: " + err.Error())
				} else {
					setStatus(fmt.Sprintf("saved: %d window(s) → %s", len(wins), sessionFilePath()))
				}
				return nil
			case tcell.KeyF8:
				wins, err := loadSession(sessionFilePath())
				if err != nil {
					setStatus("restore error: " + err.Error())
					return nil
				}
				wm.restore(wins)
				setStatus(fmt.Sprintf("restored: %d window(s) from %s", len(wins), sessionFilePath()))
				return nil
			case tcell.KeyF9:
				if wm.overviewOpen() {
					wm.CloseOverview()
				} else {
					wm.OpenOverview()
				}
				return nil
			}
			return event
		}
		switch event.Key() {
		case tcell.KeyCtrlC:
			if !wm.hasWindows() {
				app.Stop()
				return nil
			}
			// A window is open: forward Ctrl-C to it. Return a copy so it is not
			// pointer-equal to the original event (tview stops the app on the
			// original Ctrl-C event).
			e := *event
			return &e
		}
		return event
	})

	if err := app.Run(); err != nil {
		// Run() already tore the screen down on failure; calling Stop() again
		// would panic on an uninitialized tcell screen (close of nil channel).
		wm.closeAll()
		os.Exit(1)
	}
	wm.closeAll()
	os.Exit(0)
}
