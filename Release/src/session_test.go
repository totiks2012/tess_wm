package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/rivo/tview"
)

func TestSessionV2RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.conf")
	wins := []SessionWindow{
		{Kind: kindShell, Argv: []string{"/bin/bash"}, Path: "/tmp", Name: "shell"},
		{Kind: kindProg, Argv: []string{"/usr/bin/afm", "/tmp/a|b", "line\nbreak"}, Path: "/tmp/a|b", Name: "afm"},
		{Kind: kindWidget, Path: "", Name: "widget"},
	}
	if err := saveSession(path, wins); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if !strings.HasPrefix(line, "{") {
			t.Fatalf("v2 line must be a JSON object, got %q", line)
		}
	}

	got, err := loadSession(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, wins) {
		t.Fatalf("round trip = %#v, want %#v", got, wins)
	}
}

func TestSessionLegacyV1(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.conf")
	text := "shell|bash|/root|shell\n" +
		"widget|||widget\n" +
		"garbage line without pipes\n" +
		"prog|afm||afm\n"
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := loadSession(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Fatalf("legacy load = %#v, want 3 slots (garbage skipped)", got)
	}
	if got[0].Kind != kindShell || got[0].Path != "/root" || !reflect.DeepEqual(got[0].Argv, []string{"/bin/bash"}) {
		t.Fatalf("legacy shell = %#v", got[0])
	}
	if got[1].Kind != kindWidget || got[1].Name != "widget" {
		t.Fatalf("legacy widget = %#v", got[1])
	}
	if got[2].Kind != kindProg || !reflect.DeepEqual(got[2].Argv, []string{"afm"}) {
		t.Fatalf("legacy prog = %#v", got[2])
	}
}

func TestSessionInvalidJSONSkipped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "session.conf")
	text := "{not json}\n" +
		`{"kind":"bogus","path":"/tmp"}` + "\n" +
		`{"kind":"prog","argv":[],"path":"/tmp"}` + "\n" +
		`{"kind":"prog","argv":["/usr/bin/afm"],"path":"/tmp","name":"afm"}` + "\n"
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := loadSession(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Kind != kindProg {
		t.Fatalf("invalid slots must be skipped, got %#v", got)
	}
}

func TestRestoreArgvAFM(t *testing.T) {
	dir := t.TempDir()

	cases := []struct {
		name string
		in   SessionWindow
		want []string
	}{
		{
			name: "afm without arguments gets the saved path",
			in:   SessionWindow{Kind: kindProg, Argv: []string{"/root/.local/bin/afm/afm_linux_x86_64"}, Path: dir},
			want: []string{"/root/.local/bin/afm/afm_linux_x86_64", dir},
		},
		{
			name: "afm with an explicit directory keeps its argv",
			in:   SessionWindow{Kind: kindProg, Argv: []string{"/usr/bin/afm", "/other"}, Path: dir},
			want: []string{"/usr/bin/afm", "/other"},
		},
		{
			name: "non-afm program is untouched",
			in:   SessionWindow{Kind: kindProg, Argv: []string{"micro", "file.txt"}, Path: dir},
			want: []string{"micro", "file.txt"},
		},
		{
			name: "missing saved directory is not appended",
			in:   SessionWindow{Kind: kindProg, Argv: []string{"afm"}, Path: filepath.Join(dir, "nope")},
			want: []string{"afm"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := restoreArgv(c.in); !reflect.DeepEqual(got, c.want) {
				t.Fatalf("restoreArgv = %#v, want %#v", got, c.want)
			}
		})
	}
}

func TestOpenProgramAtMissingBinary(t *testing.T) {
	wm := newWM(tviewApp(), nil)
	if wm.openProgramAt([]string{"/nonexistent/wmtv-program"}, "/tmp") {
		t.Fatal("missing binary must not open a window")
	}
}

// TestWMSnapshotCapturesForegroundProgram proves F7's data path end-to-end: a
// real `sleep` running in a shell window becomes a `prog` slot with its argv
// and live cwd.
func TestWMSnapshotCapturesForegroundProgram(t *testing.T) {
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("sleep not found")
	}
	wm := newWM(tviewApp(), tview.NewBox())
	defer wm.closeAll()
	wm.openShellAt("/tmp")

	wm.mu.Lock()
	sw := wm.wins[0].shell
	wm.mu.Unlock()
	if _, err := sw.ptmx.Write([]byte("sleep 60\r")); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		snap := wm.snapshot()
		if len(snap) == 1 && snap[0].Kind == kindProg &&
			len(snap[0].Argv) > 0 && strings.HasSuffix(snap[0].Argv[0], "sleep") {
			if snap[0].Path != "/tmp" {
				t.Fatalf("saved path = %q, want /tmp", snap[0].Path)
			}
			if _, err := sw.ptmx.Write([]byte{0x03}); err != nil {
				t.Fatal(err)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("snapshot did not capture the foreground program: %#v", snap)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// TestWMRestoreProgramSlot proves F8's data path: a `prog` slot is relaunched
// with its argv in its saved directory, and no shell fallback happens.
func TestWMRestoreProgramSlot(t *testing.T) {
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("sleep not found")
	}
	wm := newWM(tviewApp(), tview.NewBox())
	defer wm.closeAll()

	fb := wm.restore([]SessionWindow{{Kind: kindProg, Argv: []string{"sleep", "60"}, Path: "/tmp", Name: "sleep"}})
	if fb != 0 {
		t.Fatalf("restore fallbacks = %d, want 0", fb)
	}
	wm.mu.Lock()
	if len(wm.wins) != 1 {
		wm.mu.Unlock()
		t.Fatalf("restore opened %d windows, want 1", len(wm.wins))
	}
	sw := wm.wins[0].shell
	wm.mu.Unlock()
	if sw == nil {
		t.Fatal("restored window must be a pty program window")
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		argv, cwd, atPrompt := sw.Foreground()
		if !atPrompt && len(argv) > 0 && strings.HasSuffix(argv[0], "sleep") {
			if cwd != "/tmp" {
				t.Fatalf("restored program cwd = %q, want /tmp", cwd)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("restored program not detected: argv=%v cwd=%q atPrompt=%v", argv, cwd, atPrompt)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
