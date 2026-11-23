package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Session is a saved workspace: an ordered list of window contexts.
//
// v2 format: one JSON object per line (JSON Lines) — argv may contain the '|'
// delimiter or newlines, which the old pipe format could not survive:
//
//	{"kind":"shell","argv":["/bin/bash"],"path":"/root","name":"shell"}
//	{"kind":"prog","argv":["/usr/bin/afm","/tmp/x"],"path":"/tmp/x","name":"afm"}
//	{"kind":"widget","path":"","name":"widget"}
//
// v1 files (kind|cmd|path|name, inspired by Txmux's "slot = cmd|path|name")
// are still read for backward compatibility.
const (
	kindShell  = "shell"
	kindProg   = "prog"
	kindWidget = "widget"
)

type SessionWindow struct {
	Kind string
	Argv []string
	Path string
	Name string
}

// sessionSlot is the on-disk v2 representation.
type sessionSlot struct {
	Kind string   `json:"kind"`
	Argv []string `json:"argv,omitempty"`
	Path string   `json:"path"`
	Name string   `json:"name"`
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
		slot := sessionSlot{Kind: w.Kind, Argv: w.Argv, Path: w.Path, Name: w.Name}
		data, err := json.Marshal(slot)
		if err != nil {
			return err
		}
		b.Write(data)
		b.WriteByte('\n')
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
		if w, ok := parseSessionLine(line); ok {
			wins = append(wins, w)
		}
	}
	return wins, nil
}

// parseSessionLine accepts either a v2 JSON object or a legacy pipe line.
func parseSessionLine(line string) (SessionWindow, bool) {
	if strings.HasPrefix(line, "{") {
		var slot sessionSlot
		if err := json.Unmarshal([]byte(line), &slot); err != nil {
			return SessionWindow{}, false
		}
		return normalizeSessionWindow(SessionWindow{
			Kind: slot.Kind,
			Argv: slot.Argv,
			Path: slot.Path,
			Name: slot.Name,
		})
	}
	parts := strings.Split(line, "|")
	if len(parts) != 4 {
		return SessionWindow{}, false
	}
	return normalizeSessionWindow(SessionWindow{
		Kind: parts[0],
		Argv: []string{parts[1]},
		Path: parts[2],
		Name: parts[3],
	})
}

// normalizeSessionWindow validates a slot and fills defaults: shell slots
// always run the interactive shell, program slots need argv, widget slots need
// neither.
func normalizeSessionWindow(w SessionWindow) (SessionWindow, bool) {
	switch w.Kind {
	case kindShell:
		w.Argv = []string{"/bin/bash"}
		return w, true
	case kindProg:
		if len(w.Argv) == 0 || w.Argv[0] == "" {
			return SessionWindow{}, false
		}
		return w, true
	case kindWidget:
		w.Argv = nil
		return w, true
	}
	return SessionWindow{}, false
}

// describeSessionWindow is a short human-readable label for status messages.
func describeSessionWindow(w SessionWindow) string {
	if w.Kind == kindProg && len(w.Argv) > 0 {
		return fmt.Sprintf("%s (%s)", programTitle(w.Argv), w.Path)
	}
	return w.Kind
}
