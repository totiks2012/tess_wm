package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Session is a saved workspace: an ordered list of window contexts.
// Format per line: kind|cmd|path|name  (POSIX-friendly, pipe-delimited).
// Inspired by Txmux's "slot = cmd|path|name", extended with a kind field.
const (
	kindShell  = "shell"
	kindWidget = "widget"
)

type SessionWindow struct {
	Kind string
	Cmd  string
	Path string
	Name string
}

func sessionFilePath() string {
	if p := os.Getenv("WM_SESSION_FILE"); p != "" {
		return p
	}
	return filepath.Join(os.Getenv("HOME"), ".config", "wm-tview", "session.conf")
}

func saveSession(path string, wins []SessionWindow) error {
	if dir := filepath.Dir(path); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	var b strings.Builder
	for _, w := range wins {
		fmt.Fprintf(&b, "%s|%s|%s|%s\n", w.Kind, w.Cmd, w.Path, w.Name)
	}
	return os.WriteFile(path, []byte(b.String()), 0o600)
}

func loadSession(path string) ([]SessionWindow, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var wins []SessionWindow
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Split(line, "|")
		if len(parts) != 4 {
			continue
		}
		wins = append(wins, SessionWindow{
			Kind: parts[0],
			Cmd:  parts[1],
			Path: parts[2],
			Name: parts[3],
		})
	}
	return wins, nil
}
