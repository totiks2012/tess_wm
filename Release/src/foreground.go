package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// procStat is the slice of /proc/<pid>/stat fields needed to find the
// foreground job of a shell's controlling terminal. Field numbering follows
// proc(5): 1 pid, 2 comm, 3 state, 4 ppid, 5 pgrp, 6 session, 7 tty_nr,
// 8 tpgid.
type procStat struct {
	PID   int
	State byte
	PPID  int
	PGRP  int
	TPGID int
}

// parseProcStat parses one /proc/<pid>/stat line. The comm field may contain
// spaces and parentheses, so fields are read after the LAST ')'.
func parseProcStat(s string) (procStat, error) {
	open := strings.IndexByte(s, '(')
	closing := strings.LastIndexByte(s, ')')
	if open <= 0 || closing < open+1 {
		return procStat{}, fmt.Errorf("malformed stat: %q", s)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(s[:open]))
	if err != nil {
		return procStat{}, fmt.Errorf("stat pid: %w", err)
	}
	fields := strings.Fields(s[closing+1:])
	if len(fields) < 6 || len(fields[0]) == 0 {
		return procStat{}, fmt.Errorf("short stat: %q", s)
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		return procStat{}, fmt.Errorf("stat ppid: %w", err)
	}
	pgrp, err := strconv.Atoi(fields[2])
	if err != nil {
		return procStat{}, fmt.Errorf("stat pgrp: %w", err)
	}
	tpgid, err := strconv.Atoi(fields[5])
	if err != nil {
		return procStat{}, fmt.Errorf("stat tpgid: %w", err)
	}
	return procStat{
		PID:   pid,
		State: fields[0][0],
		PPID:  ppid,
		PGRP:  pgrp,
		TPGID: tpgid,
	}, nil
}

func readProcStat(pid int) (procStat, error) {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return procStat{}, err
	}
	return parseProcStat(string(data))
}

// procCmdline returns a process's argv (NUL-separated /proc/<pid>/cmdline).
func procCmdline(pid int) []string {
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil || len(data) == 0 {
		return nil
	}
	parts := strings.Split(string(data), "\x00")
	for len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}
	return parts
}

// isShellProgram reports whether argv[0] is a shell, so a foreground job that
// is really another shell can be treated like a prompt (save as `shell`).
func isShellProgram(arg0 string) bool {
	name := strings.ToLower(filepath.Base(arg0))
	name = strings.TrimPrefix(name, "-") // login shell: -bash
	switch name {
	case "bash", "sh", "dash", "zsh", "fish", "ksh", "ksh93", "mksh",
		"tcsh", "csh", "ash", "busybox":
		return true
	}
	return false
}

// pickForeground chooses the program that represents a foreground process
// group. Wrappers such as `me afm` (bash -> afm) or `sh -c '...'` put several
// processes in one pgrp, so prefer a non-shell member: first a direct child of
// the group leader (the actual program), then the leader, then any non-shell.
// The second result is false when the choice is a shell (i.e. effectively a
// prompt).
func pickForeground(members []procStat, argvFor func(int) []string) (procStat, bool) {
	if len(members) == 0 {
		return procStat{}, false
	}
	leader := members[0]
	for _, m := range members {
		if m.PID == m.PGRP {
			leader = m
			break
		}
	}
	nonShell := func(m procStat) bool {
		argv := argvFor(m.PID)
		return len(argv) > 0 && !isShellProgram(argv[0])
	}
	// 1. A non-shell direct child of the group leader: the real program
	//    behind a wrapper such as `me afm` or `sh -c '...'`.
	for _, m := range members {
		if m.PPID == leader.PID && m.PID != leader.PID && nonShell(m) {
			return m, true
		}
	}
	// 2. The leader itself when it is not a shell (opencode, vim, afm...).
	if nonShell(leader) {
		return leader, true
	}
	// 3. Any other non-shell member.
	for _, m := range members {
		if nonShell(m) {
			return m, true
		}
	}
	// 4. Only shells in the group: treat as a prompt (nested shell included).
	return leader, false
}

// foregroundJob inspects a shell's controlling terminal and returns the
// foreground program's argv and cwd. atPrompt is true when the shell itself
// (or a nested shell) is in the foreground, or when the information is
// unavailable.
func foregroundJob(shellPID int) (argv []string, cwd string, atPrompt bool) {
	// A program window (restored `prog` slot) or a shell that replaced itself
	// with `exec` has the program as the pty's direct child, so it is the
	// session leader and tpgid == pgrp. Report it as a program, not a prompt.
	if own := procCmdline(shellPID); len(own) > 0 && !isShellProgram(own[0]) {
		cwd, _ = os.Readlink(fmt.Sprintf("/proc/%d/cwd", shellPID))
		return own, cwd, false
	}
	st, err := readProcStat(shellPID)
	if err != nil || st.TPGID <= 0 || st.TPGID == st.PGRP {
		return nil, "", true
	}
	pgid := st.TPGID
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, "", true
	}
	var members []procStat
	for _, ent := range entries {
		pid, err := strconv.Atoi(ent.Name())
		if err != nil {
			continue
		}
		mst, err := readProcStat(pid)
		if err != nil || mst.PGRP != pgid {
			continue
		}
		if mst.State == 'Z' || mst.State == 'X' {
			continue
		}
		members = append(members, mst)
	}
	if len(members) == 0 {
		return nil, "", true
	}
	pick, isProg := pickForeground(members, procCmdline)
	argv = procCmdline(pick.PID)
	if len(argv) == 0 {
		return nil, "", true
	}
	cwd, _ = os.Readlink(fmt.Sprintf("/proc/%d/cwd", pick.PID))
	return argv, cwd, !isProg
}

// Foreground reports the program currently in the foreground of this window's
// pty: its argv and working directory. atPrompt is true when the shell itself
// is waiting (nothing to restore but a shell in the saved directory).
func (w *shellWindow) Foreground() (argv []string, cwd string, atPrompt bool) {
	if w == nil || w.cmd == nil || w.cmd.Process == nil {
		return nil, "", true
	}
	return foregroundJob(w.cmd.Process.Pid)
}
