package main

import (
	"fmt"
	"image"
	"math"
	"sync"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

const (
	ovModeGrid = "grid"
	ovModeList = "list"

	ovCellMinW = 12
	ovCellMinH = 6
	ovPad      = 1

	ovColNormal   = tcell.ColorWhite
	ovColActive   = tcell.ColorBlue
	ovColSelected = tcell.ColorYellow
)

// winRef is a snapshot reference to one window for the overview draw pass.
type winRef struct {
	title  string
	shell  *shellWindow
	widget tview.Primitive
}

type ovLayout struct {
	mode string
	n    int // windows actually rendered (may be less than total in overflow)
	cols int
	rows int
	// extra = total windows not shown (overflow), shown as "… ещё N".
	extra int
}

// overview is the transient modal window switcher (F9): a grid of live
// thumbnails of every open window. It is deliberately NOT part of wm.wins
// (so it never appears in a session nor in its own grid). Thumbnails are
// downscaled renders of the VT buffers — the ptys themselves are never
// resized to thumbnail geometry.
type overview struct {
	*tview.Box
	wm *WM

	mu       sync.Mutex
	selected int
}

func newOverview(wm *WM) *overview {
	o := &overview{Box: tview.NewBox(), wm: wm}
	o.SetBorder(true).
		SetTitle(" overview · ↑↓←→/hjkl — выбор · Enter — вход · Esc — закрыть ")
	return o
}

// layout computes how the grid should be arranged in a span of iw×ih cells.
func (o *overview) layout(total, iw, ih int) ovLayout {
	if total <= 0 || iw <= 0 || ih <= 0 {
		return ovLayout{mode: ovModeList, n: 0, cols: 1, rows: 1}
	}
	cols := maxOv(1, int(math.Ceil(math.Sqrt(float64(total)))))
	rows := (total + cols - 1) / cols
	if cw, ch := ovCellSpan(iw, cols), ovCellSpan(ih, rows); cw >= ovCellMinW && ch >= ovCellMinH {
		return ovLayout{mode: ovModeGrid, n: total, cols: cols, rows: rows}
	}
	// Too many windows for minimum-size cells: shrink the grid if possible,
	// otherwise fall back to a title list with an overflow note.
	maxCols := maxOv(1, (iw+ovPad)/(ovCellMinW+ovPad))
	maxRows := maxOv(1, (ih+ovPad)/(ovCellMinH+ovPad))
	shown := ovMin(total, maxCols*maxRows)
	if shown == 0 {
		return ovLayout{mode: ovModeList, cols: 1, rows: 1}
	}
	return ovLayout{mode: ovModeGrid, n: shown, cols: maxCols,
		rows: (shown + maxCols - 1) / maxCols, extra: total - shown}
}

// ovCellSpan is the cell size given the span and number of columns/rows.
func ovCellSpan(span, k int) int { return (span - (k+1)*ovPad) / k }

func ovMin(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxOv(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func ovStateColor(idx, active, sel int) tcell.Color {
	switch {
	case idx == sel:
		return ovColSelected
	case idx == active:
		return ovColActive
	default:
		return ovColNormal
	}
}

func (o *overview) Draw(screen tcell.Screen) {
	o.Box.DrawForSubclass(screen, o)
	x, y, iw, ih := o.GetInnerRect()
	if iw <= 0 || ih <= 0 {
		return
	}

	wm := o.wm
	wm.mu.Lock()
	n := len(wm.wins)
	active := wm.active
	items := make([]winRef, n)
	for i, w := range wm.wins {
		items[i] = winRef{title: w.title, shell: w.shell, widget: w.content}
	}
	wm.mu.Unlock()
	if n == 0 {
		return
	}

	o.mu.Lock()
	sel := o.selected
	o.mu.Unlock()
	if sel < 0 {
		sel = 0
	}
	if sel >= n {
		sel = n - 1
	}

	lay := o.layout(n, iw, ih)
	if lay.mode == ovModeList {
		o.drawList(screen, x, y, iw, ih, items, active, sel)
		return
	}

	cw, ch := ovCellSpan(iw, lay.cols), ovCellSpan(ih, lay.rows)
	for i := 0; i < lay.n; i++ {
		cx := x + ovPad + (i%lay.cols)*(cw+ovPad)
		cy := y + ovPad + (i/lay.cols)*(ch+ovPad)
		o.drawCell(screen, items[i], i, active, sel, cx, cy, cw, ch)
	}
	if lay.extra > 0 {
		i := lay.n
		cx := x + ovPad + (i%lay.cols)*(cw+ovPad)
		cy := y + ovPad + (i/lay.cols)*(ch+ovPad)
		drawBoxFrame(screen, cx, cy, cw, ch, tcell.ColorDarkCyan,
			fmt.Sprintf(" … ещё %d ", lay.extra))
	}
}

func (o *overview) drawCell(screen tcell.Screen, it winRef, idx, active, sel, cx, cy, cw, ch int) {
	if cw <= 2 || ch <= 2 {
		return
	}
	drawBoxFrame(screen, cx, cy, cw, ch, ovStateColor(idx, active, sel),
		fmt.Sprintf(" [%d] %s ", idx, it.title))
	inner := image.Rect(cx+1, cy+1, cx+cw-1, cy+ch-1)
	if inner.Dx() <= 0 || inner.Dy() <= 0 {
		return
	}
	switch {
	case it.shell != nil:
		it.shell.renderScaled(screen, inner)
	case it.widget != nil:
		it.widget.SetRect(inner.Min.X, inner.Min.Y, inner.Dx(), inner.Dy())
		it.widget.Draw(screen)
	}
}

// drawList is the fallback view when there is no room for minimum-size cells.
func (o *overview) drawList(screen tcell.Screen, x, y, iw, ih int, items []winRef, active, sel int) {
	o.mu.Lock()
	curSel := o.selected
	o.mu.Unlock()
	line := 0
	for i := range items {
		if line >= ih {
			prefix := fmt.Sprintf("… ещё %d", len(items)-i)
			runes := []rune(prefix)
			for j, r := range runes {
				screen.SetContent(x+j, y+ih-1, r, nil, tcell.StyleDefault.Foreground(tcell.ColorDarkCyan))
			}
			break
		}
		style := tcell.StyleDefault.Foreground(ovStateColor(i, active, sel))
		if i == curSel {
			style = style.Bold(true)
		}
		mark := " "
		if i == curSel {
			mark = ">"
		}
		text := fmt.Sprintf("%s [%d] %s", mark, i, items[i].title)
		runes := []rune(text)
		if len(runes) > iw {
			runes = runes[:iw]
		}
		for j, r := range runes {
			screen.SetContent(x+j, y+line, r, nil, style)
		}
		line++
	}
}

// drawBoxFrame draws a bordered rectangle with a title, in the given color.
func drawBoxFrame(screen tcell.Screen, x, y, w, h int, col tcell.Color, title string) {
	if w < 2 || h < 2 {
		return
	}
	x2, y2 := x+w-1, y+h-1
	style := tcell.StyleDefault.Foreground(col)
	screen.SetContent(x, y, '┌', nil, style)
	screen.SetContent(x2, y, '┐', nil, style)
	screen.SetContent(x, y2, '└', nil, style)
	screen.SetContent(x2, y2, '┘', nil, style)
	for i := x + 1; i < x2; i++ {
		screen.SetContent(i, y, '─', nil, style)
		screen.SetContent(i, y2, '─', nil, style)
	}
	for j := y + 1; j < y2; j++ {
		screen.SetContent(x, j, '│', nil, style)
		screen.SetContent(x2, j, '│', nil, style)
	}
	runes := []rune(title)
	if max := w - 2; max > 0 && len(runes) > max {
		runes = runes[:max]
	}
	for i, r := range runes {
		screen.SetContent(x+1+i, y, r, nil, style.Bold(true))
	}
}

// isOvNavKey reports whether e is the arrow key k or the (lower or upper)
// hjkl letter r.
func isOvNavKey(e *tcell.EventKey, k tcell.Key, r rune) bool {
	if e.Key() == tcell.KeyRune {
		return e.Rune() == r || e.Rune() == r-32
	}
	return e.Key() == k
}

func (o *overview) InputHandler() func(*tcell.EventKey, func(tview.Primitive)) {
	return o.WrapInputHandler(func(event *tcell.EventKey, setFocus func(tview.Primitive)) {
		switch {
		case isOvNavKey(event, tcell.KeyUp, 'k'):
			o.move(event)
		case isOvNavKey(event, tcell.KeyDown, 'j'):
			o.move(event)
		case isOvNavKey(event, tcell.KeyLeft, 'h'):
			o.move(event)
		case isOvNavKey(event, tcell.KeyRight, 'l'):
			o.move(event)
		case event.Key() == tcell.KeyEnter:
			o.activate()
		case event.Key() == tcell.KeyEscape || event.Key() == tcell.KeyF4 || event.Key() == tcell.KeyF9:
			o.close()
		}
	})
}

func (o *overview) move(event *tcell.EventKey) {
	wm := o.wm
	wm.mu.Lock()
	n := len(wm.wins)
	wm.mu.Unlock()
	if n == 0 {
		return
	}
	_, _, iw, ih := o.GetInnerRect()
	lay := o.layout(n, iw, ih)

	o.mu.Lock()
	sel := o.selected
	o.mu.Unlock()

	switch {
	case isOvNavKey(event, tcell.KeyUp, 'k'):
		sel -= lay.cols
	case isOvNavKey(event, tcell.KeyDown, 'j'):
		sel += lay.cols
	case isOvNavKey(event, tcell.KeyLeft, 'h'):
		sel--
	case isOvNavKey(event, tcell.KeyRight, 'l'):
		sel++
	}
	if sel < 0 {
		sel = 0
	}
	if sel >= n {
		sel = n - 1
	}
	o.mu.Lock()
	o.selected = sel
	o.mu.Unlock()
}

func (o *overview) activate() {
	o.mu.Lock()
	sel := o.selected
	o.mu.Unlock()
	// Note: no explicit QueueUpdateDraw here or in close() — tview always
	// redraws after a key handler runs, so the state change is shown on the
	// very next frame.
	o.wm.activateOverview(sel)
}

func (o *overview) close() {
	o.wm.CloseOverview()
}
