package main

import (
	"encoding/json"
	"io"
	"log"
	"net/http/httptest"
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

func TestParseCollectionEvent(t *testing.T) {
	line := "2026-06-04 13:25:50: ACTION[Server]: Alice collects bones:bones at (229,-29031,148)"
	event, ok := parseCollectionEvent(line)
	if !ok {
		t.Fatalf("expected collection event to be parsed")
	}
	if event.Player != "Alice" || event.X != 229 || event.Y != -29031 || event.Z != 148 {
		t.Fatalf("unexpected collection event: %+v", event)
	}
}

func TestBonesCollectionRefresh(t *testing.T) {
	line := "2026-10-28 11:00:00: ACTION[Server]: Collector collects bones:bones at (-1,-29031,3)"
	event, ok := parseCollectionEvent(line)
	if !ok || event.Player != "Collector" || event.X != -1 || event.Y != -29031 || event.Z != 3 {
		t.Fatalf("unexpected collection: %+v, parsed=%v", event, ok)
	}
	for _, mode := range []string{"full", "incremental"} {
		t.Run(mode, func(t *testing.T) {
			death := "2026-10-28 10:00:00: ACTION[Server]: Owner dies at (-1,-29031,3). Bones placed\n"
			app := testAppWithLog(t, death)
			if mode == "incremental" {
				if _, err := app.refreshIncremental(); err != nil {
					t.Fatal(err)
				}
			}
			if err := os.WriteFile(app.logPath, []byte(death+line+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			refresh := app.refreshFull
			if mode == "incremental" {
				refresh = app.refreshIncremental
			}
			if _, err := refresh(); err != nil {
				t.Fatal(err)
			}
			restarted, err := newApp(app.logPath, app.statePath, app.eventsPath, app.logger)
			if err != nil {
				t.Fatal(err)
			}
			if len(restarted.events) != 1 {
				t.Fatalf("unexpected graves: %+v", restarted.events)
			}
			grave := restarted.events[0]
			if !grave.Collected || grave.Player != "Owner" || grave.CollectedBy != "Collector" || grave.CollectedAt == nil || !grave.CollectedAt.Equal(event.Timestamp) {
				t.Fatalf("collection not persisted: %+v", grave)
			}
		})
	}
}

func TestRefreshIgnoresDigLogs(t *testing.T) {
	for _, node := range []string{"mcl_core:bone_block", "bones:bones", "mcl_bones:bones"} {
		t.Run(node, func(t *testing.T) {
			line := "2026-10-28 11:00:00: ACTION[Server]: Collector digs " + node + " at (1,2,3)"
			if _, ok := parseCollectionEvent(line); ok {
				t.Fatalf("dig log parsed as collection: %s", line)
			}
			for _, mode := range []string{"full", "incremental"} {
				t.Run(mode, func(t *testing.T) {
					app := testAppWithLog(t, "2026-10-28 10:00:00: ACTION[Server]: Owner dies at (1,2,3). Bones placed\n"+line+"\n")
					refresh := app.refreshFull
					if mode == "incremental" {
						refresh = app.refreshIncremental
					}
					if _, err := refresh(); err != nil {
						t.Fatal(err)
					}
					if len(app.events) != 1 || app.events[0].Collected || app.events[0].CollectedBy != "" || app.events[0].CollectedAt != nil {
						t.Fatalf("dig log changed grave status: %+v", app.events)
					}
				})
			}
		})
	}
}

func TestFullRefreshMarksMatchingGraveAsCollected(t *testing.T) {
	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "debug.txt")
	statePath := filepath.Join(tmp, "scanner-state.json")
	eventsPath := filepath.Join(tmp, "deaths.json")
	logger := log.New(io.Discard, "", 0)
	content := "2026-04-24 17:46:02: ACTION[Server]: Mordor dies at (229,-29031,148). Bones placed\n" +
		"2026-06-04 13:25:50: ACTION[Server]: Alice collects bones:bones at (229,-29031,148)\n"
	if err := os.WriteFile(logPath, []byte(content), 0o644); err != nil {
		t.Fatalf("write log: %v", err)
	}

	app, err := newApp(logPath, statePath, eventsPath, logger)
	if err != nil {
		t.Fatalf("new app: %v", err)
	}
	if _, err := app.refreshFull(); err != nil {
		t.Fatalf("full refresh: %v", err)
	}

	if len(app.events) != 1 {
		t.Fatalf("expected one grave, got %d", len(app.events))
	}
	grave := app.events[0]
	if !grave.Collected || grave.CollectedBy != "Alice" || grave.CollectedAt == nil {
		t.Fatalf("grave was not marked as collected: %+v", grave)
	}
}

func TestCollectionMarksLatestActiveGraveAtCoordinates(t *testing.T) {
	first, _ := parseDeathEvent("2026-01-01 10:00:00: ACTION[Server]: First dies at (1,2,3). Bones placed")
	first.Collected = true
	second, _ := parseDeathEvent("2026-02-01 10:00:00: ACTION[Server]: Second dies at (1,2,3). Bones placed")
	collection, _ := parseCollectionEvent("2026-02-02 10:00:00: ACTION[Server]: Collector collects bones:bones at (1,2,3)")
	deaths := []DeathEvent{first, second}

	if !markCollected(deaths, collection) {
		t.Fatal("expected a matching grave")
	}
	if deaths[0].CollectedBy != "" || !deaths[1].Collected || deaths[1].CollectedBy != "Collector" {
		t.Fatalf("wrong grave was updated: %+v", deaths)
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

func testAppWithLog(t *testing.T, content string) *App {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "debug.txt")
	if err := os.WriteFile(logPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	app, err := newApp(logPath, filepath.Join(dir, "scanner-state.json"), filepath.Join(dir, "deaths.json"), log.New(io.Discard, "", 0))
	if err != nil {
		t.Fatal(err)
	}
	return app
}

func TestIncrementalCollectionSurvivesRestart(t *testing.T) {
	death := "2026-10-28 10:00:00: ACTION[Server]: Owner dies at (1,2,3). Bones placed\n"
	collection := "2026-10-28 11:00:00: ACTION[Server]: Collector collects bones:bones at (1,2,3)\n"
	app := testAppWithLog(t, death)
	if _, err := app.refreshIncremental(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(app.logPath, []byte(death+collection), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := app.refreshIncremental()
	if err != nil || res.Added != 0 || res.Total != 1 {
		t.Fatalf("collection-only refresh: %+v, %v", res, err)
	}
	restarted, err := newApp(app.logPath, app.statePath, app.eventsPath, app.logger)
	if err != nil {
		t.Fatal(err)
	}
	res, err = restarted.refreshIncremental()
	if err != nil || res.Added != 0 || res.Total != 1 {
		t.Fatalf("refresh after restart: %+v, %v", res, err)
	}
	recorder := httptest.NewRecorder()
	restarted.handleDeaths(recorder, httptest.NewRequest("GET", "/api/deaths", nil))
	var deaths []DeathEvent
	if err := json.Unmarshal(recorder.Body.Bytes(), &deaths); err != nil {
		t.Fatal(err)
	}
	if len(deaths) != 1 || deaths[0].Player != "Owner" || !deaths[0].Collected || deaths[0].CollectedBy != "Collector" || deaths[0].CollectedAt == nil {
		t.Fatalf("collection missing from persisted API data: %+v", deaths)
	}
	parsed, _ := parseCollectionEvent(collection[:len(collection)-1])
	if !deaths[0].CollectedAt.Equal(parsed.Timestamp) {
		t.Fatalf("unexpected collection timestamp: %v", deaths[0].CollectedAt)
	}
	if _, err := restarted.refreshFull(); err != nil {
		t.Fatal(err)
	}
	if len(restarted.events) != 1 || !restarted.events[0].Collected {
		t.Fatalf("full refresh disagrees with incremental refresh: %+v", restarted.events)
	}
}

func TestCollectionAfterLogRotationPreservesHistory(t *testing.T) {
	app := testAppWithLog(t,
		"2026-10-28 10:00:00: ACTION[Server]: Owner dies at (1,2,3). Bones placed\n"+
			"2026-10-28 10:01:00: ACTION[Server]: Other dies at (4,5,6). Bones placed\n")
	if _, err := app.refreshIncremental(); err != nil {
		t.Fatal(err)
	}
	rotated := "2026-10-28 11:00:00: ACTION[Server]: Collector collects bones:bones at (1,2,3)\n"
	if err := os.WriteFile(app.logPath, []byte(rotated), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := app.refreshIncremental()
	if err != nil || res.Added != 0 || res.Total != 2 {
		t.Fatalf("history lost on rotation: %+v, %v", res, err)
	}
	if !app.events[0].Collected || app.events[1].Collected {
		t.Fatalf("wrong collection after rotation: %+v", app.events)
	}
}

func TestCollectionMatchesLatestGraveWithinSameSecond(t *testing.T) {
	app := testAppWithLog(t,
		"2026-10-28 10:00:00: ACTION[Server]: First dies at (1,2,3). Bones placed\n"+
			"2026-10-28 10:00:00: ACTION[Server]: Second dies at (1,2,3). Bones placed\n"+
			"2026-10-28 10:00:00: ACTION[Server]: Collector collects bones:bones at (1,2,3)\n")
	if _, err := app.refreshFull(); err != nil {
		t.Fatal(err)
	}
	if len(app.events) != 2 || app.events[0].Collected || !app.events[1].Collected {
		t.Fatalf("collection did not match latest grave in log order: %+v", app.events)
	}
}

func TestCollectionDoesNotMatchUnrelatedGraves(t *testing.T) {
	for _, tc := range []struct {
		name, death string
		collected   bool
	}{
		{"different coordinates", "2026-10-28 10:00:00: ACTION[Server]: Owner dies at (4,5,6). Bones placed", false},
		{"future death", "2026-10-28 12:00:00: ACTION[Server]: Owner dies at (1,2,3). Bones placed", false},
		{"already collected", "2026-10-28 10:00:00: ACTION[Server]: Owner dies at (1,2,3). Bones placed", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			death, ok := parseDeathEvent(tc.death)
			if !ok {
				t.Fatal("invalid death fixture")
			}
			death.Collected = tc.collected
			collection, _ := parseCollectionEvent("2026-10-28 11:00:00: ACTION[Server]: Collector collects bones:bones at (1,2,3)")
			deaths := []DeathEvent{death}
			if markCollected(deaths, collection) || deaths[0].CollectedBy != "" || deaths[0].Collected != tc.collected {
				t.Fatalf("unrelated grave modified: %+v", deaths)
			}
		})
	}
}

func TestParseCollectionEventInvalid(t *testing.T) {
	for _, line := range []string{
		"2026-10-28 11:00:00: ACTION[Server]: Collector punches bones:bones at (1,2,3)",
		"2026-10-28 11:00:00: ACTION[Server]: Collector takes items from bones:bones at (1,2,3)",
		"2026-10-28 11:00:00: ACTION[Server]: Collector collects mcl_core:stone at (1,2,3)",
		"2026-99-28 11:00:00: ACTION[Server]: Collector collects bones:bones at (1,2,3)",
		"2026-10-28 11:00:00: ACTION[Server]: Collector collects bones:bones at (1,2,3) extra",
		"2026-10-28 11:00:00: ACTION[Server]: Collector digs mcl_core:stone at (1,2,3)",
		"2026-10-28 11:00:00: ACTION[Server]: Collector collects bones:bones at (99999999999999999999999,2,3)",
	} {
		if _, ok := parseCollectionEvent(line); ok {
			t.Errorf("unexpected collection parsed: %s", line)
		}
	}
}

func TestEmptyDeathsAPIIsArray(t *testing.T) {
	app := testAppWithLog(t, "")
	if _, err := app.refreshFull(); err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	app.handleDeaths(recorder, httptest.NewRequest("GET", "/api/deaths", nil))
	if recorder.Body.String() != "[]\n" {
		t.Fatalf("empty UI requires an array, got %s", recorder.Body.String())
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
