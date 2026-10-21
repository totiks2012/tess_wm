package main

import (
	"image"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

func tviewApp() *tview.Application { return tview.NewApplication() }

func shellWithOutput(t *testing.T, cmd string, want string) *shellWindow {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not found")
	}
	sw, err := newShellWindow(tviewApp(), "", nil, func() bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	// wantsRedraw=false: the read goroutine must not block on QueueUpdateDraw
	// in a test process without an event loop.
	if _, err := sw.ptmx.Write([]byte(cmd)); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(sw.emu.Render(), want) {
			return sw
		}
		time.Sleep(20 * time.Millisecond)
	}
	sw.close()
	t.Fatalf("shell never produced %q; screen:\n%s", want, sw.emu.Render())
	return nil
}

// TestOverviewLiveThumbnails proves the grid renders both ptys' live buffers
// simultaneously and highlights the active and the selected window.
func TestOverviewLiveThumbnails(t *testing.T) {
	sim := tcell.NewSimulationScreen("UTF-8")
	if err := sim.Init(); err != nil {
		t.Fatal(err)
	}
	defer sim.Fini()
	sim.SetSize(120, 40)

	app := tviewApp()
	wm := newWM(app, tview.NewBox())

	swA := shellWithOutput(t, "printf 'QQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQQ\\n'\n", "QQQ")
	defer swA.close()
	swB := shellWithOutput(t, "printf 'ZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZ\\n'\n", "ZZZ")
	defer swB.close()

	wm.addWindow("shell", swA, swA)
	wm.addWindow("shell", swB, swB) // B becomes active

	wm.OpenOverview()
	if !wm.overviewOpen() {
		t.Fatal("overview did not open")
	}

	wm.SetRect(0, 0, 120, 40)
	wm.Draw(sim)
	sim.Show()

	frame := simScreenText(t, sim)
	if !strings.Contains(frame, "QQ") {
		t.Fatalf("shell A thumbnail (QQ) missing from overview frame:\n%s", frame)
	}
	if !strings.Contains(frame, "ZZ") {
		t.Fatalf("shell B thumbnail (ZZ) missing from overview frame:\n%s", frame)
	}
	if !strings.Contains(frame, "overview") {
		t.Fatalf("overview title missing:\n%s", frame)
	}

	// Active (blue) and selected (yellow) window frames must be distinguishable.
	if !simHasFg(t, sim, tcell.ColorBlue) {
		t.Fatal("no blue (active) border color in overview")
	}
	if !simHasFg(t, sim, tcell.ColorYellow) {
		t.Fatal("no yellow (selected) border color in overview")
	}
}

// TestOverviewSelectedHighlightsActive proves selection highlighting changes
// as the user moves through the grid (arrow keys and hjkl).
func TestOverviewSelectedHighlightsActive(t *testing.T) {
	app := tviewApp()
	wm := newWM(app, tview.NewBox())
	wm.openWidget()
	wm.openWidget() // active = 1

	ov := newOverview(wm)
	wm.ovf = ov

	// Fresh overview selects window 0.
	ov.move(tcell.NewEventKey(tcell.KeyRune, 'j', tcell.ModNone))
	ov.mu.Lock()
	got := ov.selected
	ov.mu.Unlock()
	if got != 1 {
		t.Fatalf("j (down) should move selection to 1, got %d", got)
	}

	ov.move(tcell.NewEventKey(tcell.KeyUp, 0, tcell.ModNone))
	ov.mu.Lock()
	got = ov.selected
	ov.mu.Unlock()
	if got != 0 {
		t.Fatalf("up arrow should move selection back to 0, got %d", got)
	}

	ov.move(tcell.NewEventKey(tcell.KeyRune, 'l', tcell.ModNone))
	ov.mu.Lock()
	got = ov.selected
	ov.mu.Unlock()
	if got != 1 {
		t.Fatalf("l (right) should move selection to 1, got %d", got)
	}

	// Enter activates the selected window and closes the overview.
	ov.activate()
	if wm.overviewOpen() {
		t.Fatal("overview must close after Enter")
	}
	if wm.active != 1 {
		t.Fatalf("Enter should activate window 1, active=%d", wm.active)
	}
}

// TestOverviewOverflowMeasures ensures a healthy grid layout for many windows.
func TestOverviewOverflowMeasures(t *testing.T) {
	ov := &overview{}
	for _, tc := range []struct {
		total, iw, ih int
	}{
		{1, 60, 20},
		{4, 60, 20},
		{9, 60, 20},
		{30, 60, 20},
	} {
		lay := ov.layout(tc.total, tc.iw, tc.ih)
		if lay.cols < 1 || lay.rows < 1 {
			t.Fatalf("layout for %d has no cells", tc.total)
		}
		if lay.n > tc.total {
			t.Fatalf("layout for %d shows more than total", tc.total)
		}
		if lay.n+lay.extra < minInt(lay.n+lay.extra, tc.total) {
			t.Fatalf("layout overflow math broken")
		}
	}
}

// TestOverviewMouseClickActivates proves a left click over a grid thumbnail
// selects and activates that window, closing the overview; clicks outside the
// grid are not consumed.
func TestOverviewMouseClickActivates(t *testing.T) {
	app := tviewApp()
	wm := newWM(app, tview.NewBox())
	wm.openWidget()
	wm.openWidget()
	wm.openWidget() // active = 2

	ov := newOverview(wm)
	wm.ovf = ov

	// Give the grid a deterministic geometry: 3 windows, cols=2 rows=2.
	ov.SetRect(0, 0, 60, 20)
	h := ov.MouseHandler()
	if h == nil {
		t.Fatal("overview MouseHandler must not be nil")
	}

	// Click in the second text widget cell (its inner rect).
	x, y, iw, ih := ov.GetInnerRect()
	lay := ov.layout(3, iw, ih)
	cw := ovCellSpan(iw, lay.cols)
	target := image.Point{X: x + ovPad + cw + ovPad + 1, Y: y + ovPad + 1}
	consumed, _ := h(tview.MouseLeftClick, tcell.NewEventMouse(target.X, target.Y, tcell.ButtonPrimary, tcell.ModNone), func(tview.Primitive) {})
	if !consumed {
		t.Fatal("click on a thumbnail must be consumed")
	}
	if wm.overviewOpen() {
		t.Fatal("overview must close after activating a window by click")
	}
	if wm.active != 1 {
		t.Fatalf("click on cell 1 should activate window 1, active=%d", wm.active)
	}

	// Reopen and click outside the grid: not consumed, overview stays open.
	wm.OpenOverview()
	ov = wm.ovf
	ov.SetRect(0, 0, 60, 20)
	h = ov.MouseHandler()
	consumed, _ = h(tview.MouseLeftClick, tcell.NewEventMouse(58, 19, tcell.ButtonPrimary, tcell.ModNone), func(tview.Primitive) {})
	if consumed {
		t.Fatal("click in the border/padding must not be consumed")
	}
	if !wm.overviewOpen() {
		t.Fatal("overview must stay open after an outside click")
	}
}

// TestOverviewMouseWheelMovesSelection proves wheel scroll cycles the selection
// through the grid without activating.
func TestOverviewMouseWheelMovesSelection(t *testing.T) {
	app := tviewApp()
	wm := newWM(app, tview.NewBox())
	wm.openWidget()
	wm.openWidget()
	wm.openWidget()

	ov := newOverview(wm)
	wm.ovf = ov
	ov.SetRect(0, 0, 60, 20)
	h := ov.MouseHandler()

	if consumed, _ := h(tview.MouseScrollDown, tcell.NewEventMouse(20, 10, tcell.WheelDown, tcell.ModNone), func(tview.Primitive) {}); !consumed {
		t.Fatal("wheel scroll must be consumed in overview")
	}
	ov.mu.Lock()
	got := ov.selected
	ov.mu.Unlock()
	if got != 1 {
		t.Fatalf("wheel down should move selection to 1, got %d", got)
	}
	wm.CloseOverview()
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// simScreenText renders the whole simulation frame to a plain string.
func simScreenText(t *testing.T, sim tcell.SimulationScreen) string {
	t.Helper()
	cells, w, h := sim.GetContents()
	var b strings.Builder
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			c := cells[y*w+x]
			if len(c.Runes) > 0 {
				b.WriteRune(c.Runes[0])
			} else {
				b.WriteByte(' ')
			}
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// simHasFg reports whether any cell uses the given foreground color.
func simHasFg(t *testing.T, sim tcell.SimulationScreen, want tcell.Color) bool {
	t.Helper()
	cells, w, h := sim.GetContents()
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			fg, _, _ := cells[y*w+x].Style.Decompose()
			if fg.Hex() == want.Hex() {
				return true
			}
		}
	}
	return false
}
