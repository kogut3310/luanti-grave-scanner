package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const storeVersion = 3

type position struct {
	X int `json:"x"`
	Y int `json:"y"`
	Z int `json:"z"`
}

type mapObservation struct {
	At       time.Time         `json:"at"`
	Present  bool              `json:"present"`
	Node     string            `json:"node"`
	Owner    string            `json:"owner,omitempty"`
	Access   string            `json:"access"`
	Metadata map[string]string `json:"metadata,omitempty"`
}

type DeathEvent struct {
	ID             string          `json:"id"`
	Timestamp      time.Time       `json:"timestamp"` // CreatedAt, or the stable first discovery time.
	Player         string          `json:"player"`
	X              int             `json:"x"`
	Y              int             `json:"y"`
	Z              int             `json:"z"`
	RawLine        string          `json:"raw_line,omitempty"`
	Discovered     time.Time       `json:"discovered_at"`
	CreatedAt      *time.Time      `json:"created_at,omitempty"`
	LogID          string          `json:"log_id,omitempty"`
	LogCollectedAt *time.Time      `json:"log_collected_at,omitempty"`
	LogCollectedBy string          `json:"log_collected_by,omitempty"`
	SupersededAt   *time.Time      `json:"superseded_at,omitempty"`
	MapReplacedAt  *time.Time      `json:"map_replaced_at,omitempty"`
	Map            *mapObservation `json:"map,omitempty"`
	OwnerSource    string          `json:"owner_source,omitempty"`
	Status         string          `json:"status"`
	Sources        []string        `json:"sources"`
	DateSource     string          `json:"date_source"`
	Collected      bool            `json:"collected"`
	CollectedAt    *time.Time      `json:"collected_at,omitempty"`
	CollectedBy    string          `json:"collected_by,omitempty"`
	RemovalSource  string          `json:"removal_source,omitempty"`
}

func (g DeathEvent) pos() position { return position{g.X, g.Y, g.Z} }

type collectionEvent struct {
	Timestamp time.Time
	Player    string
	X, Y, Z   int
}

type storedLogEvent struct {
	ID        string    `json:"id"`
	Kind      string    `json:"kind"`
	Timestamp time.Time `json:"timestamp"`
	Player    string    `json:"player"`
	position
	RawLine string `json:"raw_line,omitempty"`
}

type scannerState struct {
	Offset        int64  `json:"offset"`
	AnchorStart   int64  `json:"anchor_start"`
	AnchorHash    string `json:"anchor_sha256,omitempty"`
	LastTimestamp string `json:"last_timestamp,omitempty"`
	FilePath      string `json:"file_path,omitempty"`
}

type scanSummary struct {
	ID         string    `json:"id"`
	Mode       string    `json:"mode"`
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	Blocks     int       `json:"blocks"`
	Found      int       `json:"found"`
	Checked    int       `json:"checked"`
}

type graveStore struct {
	Version     int              `json:"schema_version"`
	Graves      []DeathEvent     `json:"graves"`
	LogEvents   []storedLogEvent `json:"log_events"`
	Cursor      scannerState     `json:"log_cursor"`
	LastMapScan *scanSummary     `json:"last_map_scan,omitempty"`
}

func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

func cloneStore(s graveStore) graveStore {
	// Copies nested observations and slices too; a failed job never changes live data.
	data, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}
	var copy graveStore
	if err = json.Unmarshal(data, &copy); err != nil {
		panic(err)
	}
	return copy
}

func loadStore(path, legacyEvents string) (graveStore, error) {
	var s graveStore
	data, err := os.ReadFile(path)
	if err == nil {
		if err = json.Unmarshal(data, &s); err != nil {
			return s, fmt.Errorf("invalid store: %w", err)
		}
		if s.Version != storeVersion {
			return s, fmt.Errorf("unsupported store schema: %d", s.Version)
		}
		if s.Graves == nil {
			s.Graves = []DeathEvent{}
		}
		if s.LogEvents == nil {
			s.LogEvents = []storedLogEvent{}
		}
		return s, nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return s, err
	}
	s = graveStore{Version: storeVersion, Graves: []DeathEvent{}, LogEvents: []storedLogEvent{}}
	data, err = os.ReadFile(legacyEvents)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return s, err
	}
	if len(data) > 0 {
		var legacy []DeathEvent
		if err = json.Unmarshal(data, &legacy); err != nil {
			return s, fmt.Errorf("invalid legacy deaths.json: %w", err)
		}
		seen := map[string]bool{}
		for _, g := range legacy {
			e := storedLogEvent{Kind: "death", Timestamp: g.Timestamp, Player: g.Player, position: g.pos(), RawLine: g.RawLine}
			e.ID = eventID(e)
			if seen[e.ID] {
				continue
			}
			seen[e.ID] = true
			g.ID = newID()
			g.LogID = e.ID
			created := g.Timestamp
			g.CreatedAt = &created
			g.OwnerSource = "log"
			if g.Discovered.IsZero() {
				g.Discovered = time.Now()
			}
			if g.Collected {
				g.LogCollectedAt = g.CollectedAt
				g.LogCollectedBy = g.CollectedBy
				// Preserve legacy removals even if their original timestamp is missing.
				if g.LogCollectedAt == nil {
					t := g.Timestamp
					g.LogCollectedAt = &t
				}
			}
			s.Graves = append(s.Graves, g)
			s.LogEvents = append(s.LogEvents, e)
			if g.LogCollectedAt != nil {
				c := storedLogEvent{Kind: "collection", Timestamp: *g.LogCollectedAt, Player: g.LogCollectedBy, position: g.pos()}
				c.ID = eventID(c)
				if !seen[c.ID] {
					s.LogEvents = append(s.LogEvents, c)
					seen[c.ID] = true
				}
			}
		}
	}
	// Legacy offsets have no anchor: safely replay the current log on first refresh.
	reconcileLogs(&s)
	if err = persistStore(path, s); err != nil {
		return s, err
	}
	return s, nil
}

func persistStore(path string, s graveStore) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".grave-store-*.tmp")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	defer f.Close()
	if err = json.NewEncoder(f).Encode(s); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}

func derive(g *DeathEvent) {
	g.Sources = []string{}
	g.DateSource = "map_discovery"
	if g.CreatedAt != nil {
		g.Timestamp = *g.CreatedAt
		g.DateSource = "log"
	} else {
		g.Timestamp = g.Discovered
	}
	if g.CreatedAt != nil || g.LogCollectedAt != nil {
		g.Sources = append(g.Sources, "log")
	}
	if g.Map != nil {
		g.Sources = append(g.Sources, "map")
	}
	g.Status = "unverified"
	g.Collected = false
	g.CollectedAt = nil
	g.CollectedBy = ""
	g.RemovalSource = ""
	if g.Map != nil {
		if g.Map.Present {
			g.Status = "confirmed"
		} else {
			g.Status = "collected"
			t := g.Map.At
			g.CollectedAt = &t
			g.RemovalSource = "map"
		}
	}
	if g.LogCollectedAt != nil {
		g.Status = "collected"
		g.CollectedAt = g.LogCollectedAt
		g.CollectedBy = g.LogCollectedBy
		g.RemovalSource = "log"
	}
	if g.MapReplacedAt != nil && g.LogCollectedAt == nil {
		g.Status = "collected"
		g.CollectedAt = g.MapReplacedAt
		g.RemovalSource = "replacement"
	}
	if g.SupersededAt != nil && g.LogCollectedAt == nil {
		g.Status = "collected"
		g.CollectedAt = g.SupersededAt
		g.RemovalSource = "replacement"
	}
	g.Collected = g.Status == "collected"
}

func deriveStore(s *graveStore) {
	for i := range s.Graves {
		derive(&s.Graves[i])
	}
	sort.SliceStable(s.Graves, func(i, j int) bool { return s.Graves[i].Timestamp.Before(s.Graves[j].Timestamp) })
}
