package main

import (
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/rivo/tview"
)

func TestParseProcStat(t *testing.T) {
	// comm may contain spaces and parentheses; fields follow the LAST ')'.
	line := "1234 (weird (comm) name) S 1 1234 1234 34816 5678 4194304 100 0 0"
	got, err := parseProcStat(line)
	if err != nil {
		t.Fatal(err)
	}
	want := procStat{PID: 1234, State: 'S', PPID: 1, PGRP: 1234, TPGID: 5678}
	if got != want {
		t.Fatalf("parseProcStat = %+v, want %+v", got, want)
	}

	for _, bad := range []string{"", "garbage", "12 ", "12 (x) S 1 2"} {
		if _, err := parseProcStat(bad); err == nil {
			t.Errorf("parseProcStat(%q) must fail", bad)
		}
	}
}

func TestPickForeground(t *testing.T) {
	argv := map[int][]string{
		100: {"bash", "/root/.local/bin/me/me", "afm"},
		101: {"/root/.local/bin/afm/afm_linux_x86_64"},
		200: {"opencode"},
		300: {"sh", "-c", "sleep 30"},
		301: {"sleep", "30"},
		400: {"bash"},
	}
	argvFor := func(pid int) []string { return argv[pid] }

	cases := []struct {
		name     string
		members  []procStat
		wantPID  int
		wantProg bool
	}{
		{
			name: "me wrapper with afm child",
			members: []procStat{
				{PID: 100, PPID: 99, PGRP: 100, TPGID: 100},
				{PID: 101, PPID: 100, PGRP: 100, TPGID: 100},
			},
			wantPID: 101, wantProg: true,
		},
		{
			name:    "single non-shell leader",
			members: []procStat{{PID: 200, PPID: 1, PGRP: 200, TPGID: 200}},
			wantPID: 200, wantProg: true,
		},
		{
			name: "sh -c wrapper with sleep child",
			members: []procStat{
				{PID: 300, PPID: 1, PGRP: 300, TPGID: 300},
				{PID: 301, PPID: 300, PGRP: 300, TPGID: 300},
			},
			wantPID: 301, wantProg: true,
		},
		{
			name:    "nested shell only",
			members: []procStat{{PID: 400, PPID: 1, PGRP: 400, TPGID: 400}},
			wantPID: 400, wantProg: false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, isProg := pickForeground(c.members, argvFor)
			if got.PID != c.wantPID || isProg != c.wantProg {
				t.Fatalf("pickForeground = (pid %d, prog %v), want (pid %d, prog %v)",
					got.PID, isProg, c.wantPID, c.wantProg)
			}
		})
	}
}

func TestIsShellProgram(t *testing.T) {
	for _, s := range []string{"bash", "/bin/bash", "-bash", "/usr/bin/zsh", "sh", "dash"} {
		if !isShellProgram(s) {
			t.Errorf("isShellProgram(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"afm", "/usr/bin/opencode", "sleep", "vim", "micro"} {
		if isShellProgram(s) {
			t.Errorf("isShellProgram(%q) = true, want false", s)
		}
	}
}

// TestForegroundAtPrompt proves that a fresh interactive shell has nothing in
// the foreground, and that its cwd is still available for a `shell` slot.
func TestForegroundAtPrompt(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not found")
	}
	app := tview.NewApplication()
	sw, err := newShellWindow(app, "", nil, func() bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	defer sw.close()

	if _, err := sw.ptmx.Write([]byte("echo WMTV_FG_READY\r")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for !strings.Contains(shellBufferText(sw), "WMTV_FG_READY") {
		if time.Now().After(deadline) {
			t.Fatalf("bash never became ready; buffer:\n%s", shellBufferText(sw))
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Wait for bash to reap the echo job and come back to the prompt.
	for {
		argv, _, atPrompt := sw.Foreground()
		if atPrompt {
			if len(argv) != 0 {
				t.Fatalf("at prompt argv = %v, want none", argv)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("shell never returned to the prompt: argv=%v", argv)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if cwd := sw.Cwd(); cwd == "" {
		t.Fatal("shell cwd must be readable via /proc")
	}
}

// TestForegroundDetectsProgram proves the whole /proc path against a real
// foreground job: bash runs `sleep` in /tmp and Foreground reports its argv and
// cwd, which is exactly what F7 stores.
func TestForegroundDetectsProgram(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not found")
	}
	if _, err := exec.LookPath("sleep"); err != nil {
		t.Skip("sleep not found")
	}
	app := tview.NewApplication()
	sw, err := newShellWindow(app, "", nil, func() bool { return false })
	if err != nil {
		t.Fatal(err)
	}
	defer sw.close()

	if _, err := sw.ptmx.Write([]byte("cd /tmp && sleep 60\r")); err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(5 * time.Second)
	var argv []string
	var cwd string
	for {
		var atPrompt bool
		argv, cwd, atPrompt = sw.Foreground()
		if !atPrompt && len(argv) > 0 && strings.HasSuffix(argv[0], "sleep") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("foreground sleep never detected: argv=%v cwd=%q atPrompt=%v; buffer:\n%s",
				argv, cwd, atPrompt, shellBufferText(sw))
		}
		time.Sleep(20 * time.Millisecond)
	}
	if cwd != "/tmp" {
		t.Fatalf("foreground cwd = %q, want /tmp", cwd)
	}

	// Stop the job so close() does not orphan it, then tear down.
	if _, err := sw.ptmx.Write([]byte{0x03}); err != nil {
		t.Fatal(err)
	}
}
