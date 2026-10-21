package main

import (
	"image"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/vt"
	"github.com/creack/pty"
	"github.com/gdamore/tcell/v2"
)

func cellAt(t *testing.T, sim tcell.SimulationScreen, x, y int) (string, tcell.Style) {
	t.Helper()
	cells, w, h := sim.GetContents()
	if x < 0 || y < 0 || x >= w || y >= h {
		t.Fatalf("cell (%d,%d) out of %dx%d", x, y, w, h)
	}
	c := cells[y*w+x]
	return string(c.Runes), c.Style
}

// Proves the uv.Screen -> tcell.Screen adapter renders text and colors.
func TestAdapterRendersTextAndColor(t *testing.T) {
	emu := vt.NewEmulator(20, 3)
	if _, err := emu.Write([]byte("hi \x1b[31mRED\x1b[0m ok")); err != nil {
		t.Fatal(err)
	}

	sim := tcell.NewSimulationScreen("UTF-8")
	if err := sim.Init(); err != nil {
		t.Fatal(err)
	}
	defer sim.Fini()
	sim.SetSize(20, 3)

	emu.Draw(&uvAdapter{screen: sim}, image.Rect(0, 0, 20, 3))
	sim.Show()

	var line strings.Builder
	for x := 0; x < 20; x++ {
		s, _ := cellAt(t, sim, x, 0)
		if s == "" {
			s = " "
		}
		line.WriteString(s)
	}
	if got := strings.TrimRight(line.String(), " "); got != "hi RED ok" {
		t.Fatalf("rendered %q, want %q", got, "hi RED ok")
	}

	_, st := cellAt(t, sim, 4, 0) // 'R'
	fg, _, _ := st.Decompose()
	r, g, b := fg.RGB()
	if r == 0 || g != 0 || b != 0 {
		t.Fatalf("RED foreground = #%02x%02x%02x, want a red hue", r, g, b)
	}
}

// Proves a real bash in a pty, parsed by the VT emulator, yields renderable text.
func TestPTYEndToEnd(t *testing.T) {
	cmd := exec.Command("/bin/bash")
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")
	ptmx, err := pty.Start(cmd)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = ptmx.Close()
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
	}()

	emu := vt.NewSafeEmulator(40, 10)
	go func() {
		buf := make([]byte, 4096)
		for {
			n, rerr := ptmx.Read(buf)
			if n > 0 {
				_, _ = emu.Write(buf[:n])
			}
			if rerr != nil {
				return
			}
		}
	}()

	if _, err := ptmx.Write([]byte("echo WMTV_OK\n")); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(emu.Render(), "WMTV_OK") {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("bash output never captured; screen:\n%s", emu.Render())
}
