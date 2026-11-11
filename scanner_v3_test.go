package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/klauspost/compress/zstd"
)

// Format-29 fixtures reproduce the field layout and translation strings returned
// by the user's read-only diagnostic, including the old grave's missing owner.
func fixtureBlock(t *testing.T, graves map[int]map[string]string) []byte {
	t.Helper()
	var raw bytes.Buffer
	number := func(n any) {
		t.Helper()
		if err := binary.Write(&raw, binary.BigEndian, n); err != nil {
			t.Fatal(err)
		}
	}
	str16 := func(s string) { number(uint16(len(s))); raw.WriteString(s) }
	number(uint8(3))
	number(uint16(65535))
	number(uint32(31269757))
	number(uint8(0))
	number(uint16(2))
	number(uint16(0))
	str16("air")
	number(uint16(1))
	str16("bones:bones")
	number(uint8(2))
	number(uint8(2))
	for i := 0; i < 4096; i++ {
		id := uint16(0)
		if _, ok := graves[i]; ok {
			id = 1
		}
		number(id)
	}
	raw.Write(make([]byte, 8192))
	metadataCount := 0
	for _, fields := range graves {
		if len(fields) > 0 {
			metadataCount++
		}
	}
	if metadataCount == 0 {
		number(uint8(0))
	} else {
		number(uint8(2))
		number(uint16(metadataCount))
		for index, fields := range graves {
			if len(fields) == 0 {
				continue
			}
			number(uint16(index))
			number(uint32(len(fields)))
			for key, value := range fields {
				str16(key)
				number(uint32(len(value)))
				raw.WriteString(value)
				number(uint8(0))
			}
			raw.WriteString("List main 1\nWidth 1\nItem default:stone\nEndInventoryList\nEndInventory\n")
		}
	}
	number(uint8(0))
	number(uint16(0))
	number(uint8(10))
	number(uint16(0))
	encoder, err := zstd.NewWriter(nil)
	if err != nil {
		t.Fatal(err)
	}
	defer encoder.Close()
	return append([]byte{29}, encoder.EncodeAll(raw.Bytes(), nil)...)
}
func fixtureDB(t *testing.T, blocks map[position][]byte) (string, *sql.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "map.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if _, err = db.Exec("CREATE TABLE blocks(x INTEGER,y INTEGER,z INTEGER,data BLOB NOT NULL,PRIMARY KEY(x,z,y))"); err != nil {
		t.Fatal(err)
	}
	for p, data := range blocks {
		if _, err = db.Exec("INSERT INTO blocks VALUES(?,?,?,?)", p.X, p.Y, p.Z, data); err != nil {
			t.Fatal(err)
		}
	}
	return path, db
}
func runMap(t *testing.T, a *App, mode string) scanJob {
	t.Helper()
	w := httptest.NewRecorder()
	a.handleMapStart(w, httptest.NewRequest("POST", "/api/map/"+mode, nil), mode)
	if w.Code != 202 {
		t.Fatalf("start: %d %s", w.Code, w.Body)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		a.jobMu.Lock()
		job := *a.job
		a.jobMu.Unlock()
		if job.State != "running" { // Wait until the serialization lock is released too.
			for !a.scanMu.TryLock() {
				time.Sleep(time.Millisecond)
			}
			a.scanMu.Unlock()
			return job
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("map job timed out")
	return scanJob{}
}
func utc(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestUserMetadataAndNegativeCoordinates(t *testing.T) {
	fresh := position{68, -29034, 24}
	old := position{476, 19, -3}
	for _, tc := range []struct {
		p             position
		fields        map[string]string
		owner, access string
	}{
		{fresh, map[string]string{"owner": "Miki", "time": "100", "infotext": "\x1b(T@bones)\x1bFMiki\x1bE's fresh bones\x1bE"}, "Miki", "private"},
		{old, map[string]string{"time": "1194", "infotext": "\x1b(T@bones)\x1bFMordor\x1bE's old bones\x1bE"}, "Mordor", "public"},
	} {
		path, _ := fixtureDB(t, map[position][]byte{blockOf(tc.p): fixtureBlock(t, map[int]map[string]string{indexOf(tc.p): tc.fields})})
		before, _ := os.ReadFile(path)
		app := testAppWithLog(t, "")
		app.mapPath = path
		job := runMap(t, app, "full")
		if job.State != "succeeded" {
			t.Fatalf("job: %+v", job)
		}
		s := app.snapshot()
		if len(s.Graves) != 1 {
			t.Fatalf("graves: %+v", s.Graves)
		}
		g := s.Graves[0]
		if g.Player != tc.owner || g.Map.Access != tc.access || g.pos() != tc.p || g.Status != "confirmed" || g.CreatedAt != nil {
			t.Fatalf("wrong grave: %+v", g)
		}
		stableID, stableDate := g.ID, g.Timestamp
		if job = runMap(t, app, "full"); job.State != "succeeded" {
			t.Fatal(job.Error)
		}
		g = app.snapshot().Graves[0]
		if g.ID != stableID || !g.Timestamp.Equal(stableDate) || len(app.snapshot().Graves) != 1 {
			t.Fatalf("repeat scan duplicated/redated grave: %+v", g)
		}
		after, _ := os.ReadFile(path)
		if sha256.Sum256(before) != sha256.Sum256(after) {
			t.Fatal("source database changed")
		}
	}
	if blockOf(fresh) != (position{4, -1815, 1}) || blockOf(old) != (position{29, 1, -1}) {
		t.Fatal("negative division is incorrect")
	}
}

func TestMapRemovesAndLogLaterProvidesCollector(t *testing.T) {
	p := position{474, 7, 4}
	path, _ := fixtureDB(t, map[position][]byte{blockOf(p): fixtureBlock(t, nil)})
	app := testAppWithLog(t, "2026-01-01 10:00:00: ACTION[Server]: Miki dies at (474,7,4). Bones placed\n")
	app.mapPath = path
	if job := runMap(t, app, "verify"); job.State != "succeeded" {
		t.Fatal(job.Error)
	}
	g := app.snapshot().Graves[0]
	if !g.Collected || g.RemovalSource != "map" || g.Map.Node != "air" || g.CollectedBy != "" {
		t.Fatalf("absence: %+v", g)
	}
	f, _ := os.OpenFile(app.logPath, os.O_APPEND|os.O_WRONLY, 0600)
	_, _ = f.WriteString("2026-01-02 10:00:00: ACTION[Server]: Collector collects bones:bones at (474,7,4)\n")
	f.Close()
	if _, err := app.refreshIncremental(); err != nil {
		t.Fatal(err)
	}
	g = app.snapshot().Graves[0]
	if g.CollectedBy != "Collector" || g.RemovalSource != "log" || g.CollectedAt.Year() != 2026 {
		t.Fatalf("collector: %+v", g)
	}
}

func TestMapMetadataEnrichmentThenHistoricalLogMerge(t *testing.T) {
	p := position{68, -29034, 24}
	path, db := fixtureDB(t, map[position][]byte{blockOf(p): fixtureBlock(t, map[int]map[string]string{indexOf(p): nil})})
	app := testAppWithLog(t, "")
	app.mapPath = path
	if job := runMap(t, app, "full"); job.State != "succeeded" {
		t.Fatal(job.Error)
	}
	before := app.snapshot().Graves[0]
	data := fixtureBlock(t, map[int]map[string]string{indexOf(p): {"owner": "Miki", "infotext": "\x1b(T@bones)\x1bFMiki\x1bE's fresh bones\x1bE"}})
	if _, err := db.Exec("UPDATE blocks SET data=?", data); err != nil {
		t.Fatal(err)
	}
	if job := runMap(t, app, "verify"); job.State != "succeeded" {
		t.Fatal(job.Error)
	}
	after := app.snapshot().Graves[0]
	if after.Player != "Miki" || after.ID != before.ID || !after.Timestamp.Equal(before.Timestamp) {
		t.Fatalf("enrichment: %+v", after)
	}
	if err := os.WriteFile(app.logPath, []byte("2026-01-01 10:00:00: ACTION[Server]: Miki dies at (68,-29034,24). Bones placed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := app.refreshFull(); err != nil {
		t.Fatal(err)
	}
	s := app.snapshot()
	after = s.Graves[0]
	if len(s.Graves) != 1 || after.ID != before.ID || after.CreatedAt == nil || after.DateSource != "log" || after.Timestamp.Year() != 2026 || !after.Discovered.Equal(before.Discovered) {
		t.Fatalf("historical merge: %+v", s.Graves)
	}
	if _, err := app.refreshFull(); err != nil {
		t.Fatal(err)
	}
	if len(app.snapshot().Graves) != 1 {
		t.Fatal("rescan duplicated grave")
	}
}

func TestCoordinateReuseAndLateHistoricalCollection(t *testing.T) {
	s := graveStore{Version: 3, Graves: []DeathEvent{}, LogEvents: []storedLogEvent{}}
	p := position{1, 2, 3}
	at := utc("2026-03-01T00:00:00Z")
	applyObservation(&s, p, mapObservation{At: at, Present: true, Node: "bones:bones", Owner: "Miki"})
	originalID := s.Graves[0].ID
	death := storedLogEvent{Kind: "death", Timestamp: utc("2026-01-01T00:00:00Z"), Player: "Miki", position: p}
	death.ID = eventID(death)
	collected := storedLogEvent{Kind: "collection", Timestamp: utc("2026-02-01T00:00:00Z"), Player: "Other", position: p}
	collected.ID = eventID(collected)
	mergeLogEvents(&s, []storedLogEvent{death, collected})
	if len(s.Graves) != 2 {
		t.Fatalf("old already-collected grave incorrectly merged into current map grave: %+v", s.Graves)
	}
	if s.Graves[1].ID != originalID || s.Graves[1].Collected {
		t.Fatalf("historical collection closed later grave: %+v", s.Graves)
	}
	next := storedLogEvent{Kind: "death", Timestamp: utc("2026-04-01T00:00:00Z"), Player: "Next", position: p}
	next.ID = eventID(next)
	mergeLogEvents(&s, []storedLogEvent{next})
	if len(s.Graves) != 3 || !s.Graves[1].Collected || s.Graves[2].Collected {
		t.Fatalf("new placement did not close older map grave: %+v", s.Graves)
	}
}

func TestDifferentMapOwnerReplacementSurvivesLogRefresh(t *testing.T) {
	app := testAppWithLog(t, "2026-01-01 10:00:00: ACTION[Server]: First dies at (1,2,3). Bones placed\n")
	if _, err := app.refreshIncremental(); err != nil {
		t.Fatal(err)
	}
	s := app.snapshot()
	applyObservation(&s, position{1, 2, 3}, mapObservation{At: time.Now(), Present: true, Node: "bones:bones", Owner: "Second"})
	deriveStore(&s)
	if err := app.commit(s); err != nil {
		t.Fatal(err)
	}
	if _, err := app.refreshIncremental(); err != nil {
		t.Fatal(err)
	}
	s = app.snapshot()
	if len(s.Graves) != 2 || !s.Graves[0].Collected || s.Graves[1].Collected {
		t.Fatalf("replacement disappeared during replay: %+v", s.Graves)
	}
}

func TestRotationAnchorHandlesSmallerEqualLargerFilesAndSameTimestamp(t *testing.T) {
	initial := "2026-01-01 10:00:00: ACTION[Server]: Alice dies at (1,2,3). Bones placed\n"
	for _, name := range []string{"smaller", "equal", "larger"} {
		t.Run(name, func(t *testing.T) {
			app := testAppWithLog(t, initial)
			if _, err := app.refreshIncremental(); err != nil {
				t.Fatal(err)
			}
			replacement := strings.ReplaceAll(initial, "Alice", "Carol")
			if name == "smaller" {
				replacement = strings.ReplaceAll(initial, "Alice", "Miki")
			}
			if name == "larger" {
				replacement += "a non-event line\n"
			}
			if err := os.WriteFile(app.logPath, []byte(replacement), 0600); err != nil {
				t.Fatal(err)
			}
			response, err := app.refreshIncremental()
			if err != nil {
				t.Fatal(err)
			}
			if !response.Replayed || response.Added != 1 || response.Total != 2 {
				t.Fatalf("rotation: %+v", response)
			}
			response, err = app.refreshIncremental()
			if err != nil || response.Replayed || response.Added != 0 {
				t.Fatalf("continuation: %+v %v", response, err)
			}
			response, err = app.refreshFull()
			if err != nil || response.Total != 2 || response.Added != 0 {
				t.Fatalf("idempotent full scan: %+v %v", response, err)
			}
		})
	}
}

func TestPartialLineAndFailedSaveDoNotAdvanceCursor(t *testing.T) {
	first := "2026-01-01 10:00:00: ACTION[Server]: First dies at (1,2,3). Bones placed\n"
	second := "2026-01-02 10:00:00: ACTION[Server]: Second dies at (4,5,6). Bones placed"
	app := testAppWithLog(t, first+second)
	if _, err := app.refreshIncremental(); err != nil {
		t.Fatal(err)
	}
	if app.snapshot().Cursor.Offset != int64(len(first)) || len(app.snapshot().Graves) != 1 {
		t.Fatal("partial line was consumed")
	}
	if err := os.WriteFile(app.logPath, []byte(first+second+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	savedPath := app.storePath
	blocked := filepath.Join(filepath.Dir(savedPath), "blocked")
	if err := os.WriteFile(blocked, []byte("file"), 0600); err != nil {
		t.Fatal(err)
	}
	app.storePath = filepath.Join(blocked, "store.json")
	previous := app.snapshot()
	if _, err := app.refreshIncremental(); err == nil {
		t.Fatal("expected persistence failure")
	}
	if !reflect.DeepEqual(previous, app.snapshot()) {
		t.Fatal("failed save advanced live state")
	}
	app.storePath = savedPath
	if response, err := app.refreshIncremental(); err != nil || response.Added != 1 || response.Total != 2 {
		t.Fatalf("retry lost event: %+v %v", response, err)
	}
}

func TestMigrationOncePreservesOriginalFilesAndHistory(t *testing.T) {
	dir := t.TempDir()
	legacyPath := filepath.Join(dir, "deaths.json")
	statePath := filepath.Join(dir, "scanner-state.json")
	storePath := filepath.Join(dir, "grave-store-v3.json")
	d, _ := parseDeathEvent("2026-01-01 10:00:00: ACTION[Server]: First dies at (1,2,3). Bones placed")
	d.Collected = true
	at := d.Timestamp.Add(time.Hour)
	d.CollectedAt = &at
	d.CollectedBy = "Collector"
	original, _ := json.Marshal([]DeathEvent{d})
	if err := os.WriteFile(legacyPath, original, 0600); err != nil {
		t.Fatal(err)
	}
	state := []byte(`{"offset":12345}`)
	_ = os.WriteFile(statePath, state, 0600)
	s, err := loadStore(storePath, legacyPath)
	if err != nil {
		t.Fatal(err)
	}
	if s.Version != 3 || len(s.Graves) != 1 || !s.Graves[0].Collected || s.Graves[0].CollectedBy != "Collector" || s.Cursor.Offset != 0 {
		t.Fatalf("migration: %+v", s)
	}
	for path, want := range map[string][]byte{legacyPath: original, statePath: state} {
		got, _ := os.ReadFile(path)
		if !bytes.Equal(got, want) {
			t.Fatalf("legacy file changed: %s", path)
		}
	}
	_ = os.WriteFile(legacyPath, []byte("invalid old data"), 0600)
	loaded, err := loadStore(storePath, legacyPath)
	if err != nil || len(loaded.Graves) != 1 || loaded.Graves[0].ID != s.Graves[0].ID {
		t.Fatalf("migration repeated: %+v %v", loaded, err)
	}
	_ = os.WriteFile(storePath, []byte("invalid new store"), 0600)
	if _, err = loadStore(storePath, legacyPath); err == nil {
		t.Fatal("corrupt new store was silently replaced")
	}
}

func TestReadOnlyWALSchemaAndUnsupportedBlock(t *testing.T) {
	p := position{0, 0, 0}
	path, writer := fixtureDB(t, map[position][]byte{})
	if _, err := writer.Exec("PRAGMA journal_mode=WAL"); err != nil {
		t.Fatal(err)
	}
	if _, err := writer.Exec("INSERT INTO blocks VALUES(0,0,0,?)", fixtureBlock(t, map[int]map[string]string{0: {"owner": "Miki"}})); err != nil {
		t.Fatal(err)
	}
	reader, err := openMap(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.close()
	if _, err = reader.db.Exec("DELETE FROM blocks"); err == nil {
		t.Fatal("database was writable")
	}
	row, err := reader.point(context.Background(), p)
	if err != nil || row == nil {
		t.Fatalf("WAL record unavailable: %v", err)
	}
	block, err := reader.decode(*row)
	if err != nil || block.names[block.nodes[0]] != "bones:bones" {
		t.Fatalf("WAL decode: %v", err)
	}
	for _, blob := range [][]byte{nil, {28, 1, 2, 3}, {29, 1, 2, 3}} {
		if _, _, err = decodeBlock(reader.decoder, blob, reader.scratch); err == nil {
			t.Fatalf("accepted bad block: %v", blob)
		}
	}
}

func TestFullScanPaginationAndAtomicFailure(t *testing.T) {
	blocks := map[position][]byte{}
	for i := 0; i < 130; i++ {
		blocks[position{i - 65, 0, 0}] = fixtureBlock(t, map[int]map[string]string{0: {"owner": fmt.Sprintf("Player%d", i)}})
	}
	path, writer := fixtureDB(t, blocks)
	app := testAppWithLog(t, "")
	app.mapPath = path
	if job := runMap(t, app, "full"); job.State != "succeeded" || job.Blocks != 130 || job.Found != 130 {
		t.Fatalf("pagination: %+v", job)
	}
	before := app.snapshot()
	// A valid new discovery is decoded before this malformed final block.
	_, _ = writer.Exec("INSERT INTO blocks VALUES(100,0,0,?)", fixtureBlock(t, map[int]map[string]string{0: {"owner": "New"}}))
	_, _ = writer.Exec("INSERT INTO blocks VALUES(101,0,0,?)", []byte{28})
	if job := runMap(t, app, "full"); job.State != "failed" {
		t.Fatalf("bad block accepted: %+v", job)
	}
	after := app.snapshot()
	if !reflect.DeepEqual(before.Graves, after.Graves) || !reflect.DeepEqual(before.LastMapScan, after.LastMapScan) {
		t.Fatal("failed map scan published partial results")
	}
}

func TestMapJobsRequireLogRefreshAndRejectConcurrentScans(t *testing.T) {
	app := testAppWithLog(t, "")
	path, _ := fixtureDB(t, map[position][]byte{})
	app.mapPath = path
	if err := os.Remove(app.logPath); err != nil {
		t.Fatal(err)
	}
	if job := runMap(t, app, "full"); job.State != "failed" || !strings.Contains(job.Error, "open log") {
		t.Fatalf("missing log ignored: %+v", job)
	}
	app.scanMu.Lock()
	w := httptest.NewRecorder()
	app.handleMapStart(w, httptest.NewRequest("POST", "/api/map/full", nil), "full")
	if w.Code != 409 {
		t.Fatalf("concurrent map scan: %d", w.Code)
	}
	if _, err := app.refreshIncremental(); err != errBusy {
		t.Fatalf("concurrent log scan: %v", err)
	}
	app.scanMu.Unlock()
}

func TestCancelledMapScanStopsAndPreservesSource(t *testing.T) {
	blocks := map[position][]byte{}
	for i := 0; i < 3; i++ {
		blocks[position{i, 0, 0}] = fixtureBlock(t, map[int]map[string]string{0: {"owner": "Miki"}})
	}
	path, _ := fixtureDB(t, blocks)
	before, _ := os.ReadFile(path)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	candidate := graveStore{Version: 3, Graves: []DeathEvent{}, LogEvents: []storedLogEvent{}}
	_, err := scanMap(ctx, path, "full", &candidate, func(int, int, int) { cancel() })
	if err != context.Canceled {
		t.Fatalf("cancellation ignored: %v", err)
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("cancelled scan changed source")
	}
}

func TestLateHistoricalLogDoesNotCreateSecondCurrentGrave(t *testing.T) {
	s := graveStore{Version: 3, Graves: []DeathEvent{}, LogEvents: []storedLogEvent{}}
	p := position{1, 2, 3}
	applyObservation(&s, p, mapObservation{At: utc("2026-03-01T00:00:00Z"), Present: true, Node: "bones:bones", Owner: "Current"})
	e := storedLogEvent{Kind: "death", Timestamp: utc("2026-01-01T00:00:00Z"), Player: "Historical", position: p}
	e.ID = eventID(e)
	mergeLogEvents(&s, []storedLogEvent{e})
	if len(s.Graves) != 2 || !s.Graves[0].Collected || s.Graves[1].Collected || s.Graves[0].RemovalSource != "replacement" {
		t.Fatalf("two current graves at one node: %+v", s.Graves)
	}
}

func TestCancellationAPIAndJobState(t *testing.T) {
	app := testAppWithLog(t, "")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	app.scanMu.Lock()
	app.job = &scanJob{ID: "cancel-me", Mode: "full", State: "running", Stage: "logs", StartedAt: time.Now()}
	app.cancel = cancel
	before := app.snapshot()
	w := httptest.NewRecorder()
	app.handleCancel(w, httptest.NewRequest("POST", "/api/scan/cancel", strings.NewReader(`{"id":"other-job"}`)))
	if w.Code != 409 || ctx.Err() != nil {
		t.Fatal("wrong job id cancelled running task")
	}
	w = httptest.NewRecorder()
	app.handleCancel(w, httptest.NewRequest("POST", "/api/scan/cancel", strings.NewReader(`{"id":"cancel-me"}`)))
	if w.Code != 202 || ctx.Err() != context.Canceled {
		t.Fatalf("cancel endpoint: %d %v", w.Code, ctx.Err())
	}
	app.runMapJob(ctx, "full")
	if app.job.State != "cancelled" || !reflect.DeepEqual(before, app.snapshot()) {
		t.Fatalf("cancelled job changed results: %+v", app.job)
	}
	if !app.scanMu.TryLock() {
		t.Fatal("cancelled job did not release scan lock")
	}
	app.scanMu.Unlock()
}

func TestNonGraveObservationDoesNotExportOtherNodesMetadata(t *testing.T) {
	b := decodedBlock{names: map[int]string{0: "mcl_chests:chest"}, nodes: []int{0}, metadata: map[int]map[string]string{0: {"owner": "Other", "book": "Unrelated private text"}}}
	observation := observation(b, 0, time.Now())
	if observation.Present || observation.Owner != "" || observation.Metadata != nil || observation.Node != "mcl_chests:chest" {
		t.Fatalf("exported unrelated metadata: %+v", observation)
	}
}
