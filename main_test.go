package main

import (
	"io"
	"log"
	"os"
	"path/filepath"
	"testing"
)

func TestParseDeathEvent(t *testing.T) {
	line := "2025-12-05 14:59:55: ACTION[Server]: Mordor dies at (23,-29035,-22). Bones placed"
	event, ok := parseDeathEvent(line)
	if !ok {
		t.Fatalf("expected event to be parsed")
	}
	if event.Player != "Mordor" {
		t.Fatalf("unexpected player: %s", event.Player)
	}
	if event.X != 23 || event.Y != -29035 || event.Z != -22 {
		t.Fatalf("unexpected coordinates: %d,%d,%d", event.X, event.Y, event.Z)
	}
}

func TestParseDeathEventInvalid(t *testing.T) {
	line := "2025-12-05 14:59:55: ACTION[Server]: Mordor joins game"
	if _, ok := parseDeathEvent(line); ok {
		t.Fatalf("expected no parse")
	}
}

func TestRefreshIncrementalAndFull(t *testing.T) {
	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "debug.txt")
	statePath := filepath.Join(tmp, "scanner-state.json")
	eventsPath := filepath.Join(tmp, "deaths.json")
	logger := log.New(io.Discard, "", 0)

	initial := "2025-12-05 14:59:55: ACTION[Server]: Mordor dies at (23,-29035,-22). Bones placed\n"
	if err := os.WriteFile(logPath, []byte(initial), 0o644); err != nil {
		t.Fatalf("write log: %v", err)
	}

	app, err := newApp(logPath, statePath, eventsPath, logger)
	if err != nil {
		t.Fatalf("new app: %v", err)
	}

	res1, err := app.refreshIncremental()
	if err != nil {
		t.Fatalf("refresh incremental #1: %v", err)
	}
	if res1.Added != 1 || res1.Total != 1 {
		t.Fatalf("unexpected res1: %+v", res1)
	}

	appendLine := "2025-12-06 10:00:00: ACTION[Server]: Alice dies at (100,20,-5). Bones placed\n"
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("open append: %v", err)
	}
	if _, err := f.WriteString(appendLine); err != nil {
		_ = f.Close()
		t.Fatalf("append line: %v", err)
	}
	_ = f.Close()

	res2, err := app.refreshIncremental()
	if err != nil {
		t.Fatalf("refresh incremental #2: %v", err)
	}
	if res2.Added != 1 || res2.Total != 2 {
		t.Fatalf("unexpected res2: %+v", res2)
	}

	fullContent := initial + appendLine + "2025-12-07 09:00:00: ACTION[Server]: Bob dies at (1,2,3). Bones placed\n"
	if err := os.WriteFile(logPath, []byte(fullContent), 0o644); err != nil {
		t.Fatalf("rewrite full log: %v", err)
	}

	resFull, err := app.refreshFull()
	if err != nil {
		t.Fatalf("refresh full: %v", err)
	}
	if resFull.Mode != "full" || resFull.Total != 3 || resFull.Added != 3 {
		t.Fatalf("unexpected full response: %+v", resFull)
	}
}

func TestRefreshIncrementalHandlesTruncation(t *testing.T) {
	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "debug.txt")
	statePath := filepath.Join(tmp, "scanner-state.json")
	eventsPath := filepath.Join(tmp, "deaths.json")
	logger := log.New(io.Discard, "", 0)

	first := "2025-12-05 14:59:55: ACTION[Server]: Mordor dies at (23,-29035,-22). Bones placed\n"
	if err := os.WriteFile(logPath, []byte(first), 0o644); err != nil {
		t.Fatalf("write first: %v", err)
	}

	app, err := newApp(logPath, statePath, eventsPath, logger)
	if err != nil {
		t.Fatalf("new app: %v", err)
	}
	if _, err := app.refreshIncremental(); err != nil {
		t.Fatalf("first refresh: %v", err)
	}

	truncated := "2025-12-06 10:00:00: ACTION[Server]: Alice dies at (1,2,3). Bones placed\n"
	if err := os.WriteFile(logPath, []byte(truncated), 0o644); err != nil {
		t.Fatalf("truncate rewrite: %v", err)
	}

	res, err := app.refreshIncremental()
	if err != nil {
		t.Fatalf("refresh after truncation: %v", err)
	}
	if res.Added != 1 || res.Total != 2 {
		t.Fatalf("unexpected response after truncation: %+v", res)
	}
}

func TestRefreshDoesNotModifySourceLog(t *testing.T) {
	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "debug.txt")
	statePath := filepath.Join(tmp, "scanner-state.json")
	eventsPath := filepath.Join(tmp, "deaths.json")
	logger := log.New(io.Discard, "", 0)

	content := "2025-12-05 14:59:55: ACTION[Server]: Mordor dies at (23,-29035,-22). Bones placed\n"
	if err := os.WriteFile(logPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write log: %v", err)
	}

	before, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read before: %v", err)
	}

	app, err := newApp(logPath, statePath, eventsPath, logger)
	if err != nil {
		t.Fatalf("new app: %v", err)
	}
	if _, err := app.refreshIncremental(); err != nil {
		t.Fatalf("incremental: %v", err)
	}
	if _, err := app.refreshFull(); err != nil {
		t.Fatalf("full: %v", err)
	}

	after, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read after: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("source log was modified by refresh")
	}
}
