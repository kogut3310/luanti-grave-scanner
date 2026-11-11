package main

import (
	"crypto/sha256"
	"fmt"
	"sort"
	"time"
)

func eventID(e storedLogEvent) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("%s|%s|%s|%d,%d,%d", e.Kind, e.Timestamp.Format(time.RFC3339Nano), e.Player, e.X, e.Y, e.Z))))
}

func mergeLogEvents(s *graveStore, events []storedLogEvent) int {
	known := make(map[string]bool, len(s.LogEvents))
	for _, e := range s.LogEvents {
		known[e.ID] = true
	}
	added := 0
	for _, e := range events {
		if !known[e.ID] {
			s.LogEvents = append(s.LogEvents, e)
			known[e.ID] = true
			if e.Kind == "death" {
				added++
			}
		}
	}
	reconcileLogs(s)
	return added
}

func canAttachMapGrave(g DeathEvent, e storedLogEvent, events []storedLogEvent, index int) bool {
	if g.LogID != "" || g.pos() != e.position || e.Timestamp.After(g.Discovered) || (g.Player != "" && g.Player != e.Player) {
		return false
	}
	// Another lifecycle event before this first observation proves it is a different grave.
	for _, later := range events[index+1:] {
		if later.Timestamp.After(g.Discovered) {
			break
		}
		if later.position == e.position {
			return false
		}
	}
	return true
}

func reconcileLogs(s *graveStore) {
	sort.SliceStable(s.LogEvents, func(i, j int) bool { return s.LogEvents[i].Timestamp.Before(s.LogEvents[j].Timestamp) })
	byLog := map[string]int{}
	for i := range s.Graves {
		if s.Graves[i].LogID != "" {
			byLog[s.Graves[i].LogID] = i
		}
	}
	for index, e := range s.LogEvents {
		if e.Kind != "death" {
			continue
		}
		i, ok := byLog[e.ID]
		if !ok {
			i = -1
			for j, g := range s.Graves {
				if canAttachMapGrave(g, e, s.LogEvents, index) && (i < 0 || g.Discovered.Before(s.Graves[i].Discovered)) {
					i = j
				}
			}
			if i < 0 {
				s.Graves = append(s.Graves, DeathEvent{ID: newID(), X: e.X, Y: e.Y, Z: e.Z, Discovered: time.Now()})
				i = len(s.Graves) - 1
			}
			byLog[e.ID] = i
		}
		g := &s.Graves[i]
		t := e.Timestamp
		g.CreatedAt = &t
		g.Timestamp = t
		g.LogID = e.ID
		g.Player = e.Player
		g.OwnerSource = "log"
		g.RawLine = e.RawLine
	}
	// Replay the deduplicated ledger so a late historical log can resolve orphan collections.
	for i := range s.Graves {
		s.Graves[i].LogCollectedAt = nil
		s.Graves[i].LogCollectedBy = ""
		s.Graves[i].SupersededAt = nil
	}
	active := map[position]int{}
	for _, e := range s.LogEvents {
		if e.Kind == "death" {
			i := byLog[e.ID]
			if previous, ok := active[e.position]; ok && previous != i {
				t := e.Timestamp
				s.Graves[previous].SupersededAt = &t
			}
			active[e.position] = i
		} else if e.Kind == "collection" {
			i, ok := active[e.position]
			if !ok {
				i = -1
				for j, g := range s.Graves {
					if g.LogID == "" && g.pos() == e.position && !g.Discovered.After(e.Timestamp) && g.LogCollectedAt == nil && (i < 0 || g.Discovered.After(s.Graves[i].Discovered)) {
						i = j
					}
				}
				if i < 0 {
					continue
				}
			}
			t := e.Timestamp
			s.Graves[i].LogCollectedAt = &t
			s.Graves[i].LogCollectedBy = e.Player
			delete(active, e.position)
		}
	}
	// A new placement also closes an older map-only grave at the same node.
	for _, e := range s.LogEvents {
		if e.Kind != "death" {
			continue
		}
		for i := range s.Graves {
			g := &s.Graves[i]
			if g.LogID == "" && g.pos() == e.position && !g.Discovered.After(e.Timestamp) && (g.SupersededAt == nil || e.Timestamp.Before(*g.SupersededAt)) {
				t := e.Timestamp
				g.SupersededAt = &t
			}
		}
	}
	deriveStore(s)
	// A later map-only discovery also closes newly imported historical graves.
	// This matters when a full log replay happens AFTER the first map scan.
	groups := map[position][]int{}
	for i, g := range s.Graves {
		groups[g.pos()] = append(groups[g.pos()], i)
	}
	for _, indices := range groups {
		for _, i := range indices {
			current := s.Graves[i]
			if current.CreatedAt != nil || current.Collected || current.Map == nil || !current.Map.Present {
				continue
			}
			for _, j := range indices {
				older := &s.Graves[j]
				if j != i && !older.Collected && older.Timestamp.Before(current.Discovered) {
					t := current.Discovered
					older.MapReplacedAt = &t
					derive(older)
				}
			}
		}
	}
	deriveStore(s)
}

func latestAt(s *graveStore, p position) int {
	i := -1
	for j, g := range s.Graves {
		if g.pos() == p && (i < 0 || !g.Timestamp.Before(s.Graves[i].Timestamp)) {
			i = j
		}
	}
	return i
}

func applyObservation(s *graveStore, p position, o mapObservation) {
	i := latestAt(s, p)
	if !o.Present {
		if i >= 0 {
			s.Graves[i].Map = &o
			derive(&s.Graves[i])
		}
		return
	}
	if i >= 0 {
		g := &s.Graves[i]
		if g.LogCollectedAt != nil || g.SupersededAt != nil || g.MapReplacedAt != nil || (g.Player != "" && o.Owner != "" && g.Player != o.Owner) {
			// A known removal, or a different deceased player, starts a new lifecycle.
			if !g.Collected {
				t := o.At
				g.MapReplacedAt = &t
				derive(g)
			}
			i = -1
		}
	}
	if i < 0 {
		s.Graves = append(s.Graves, DeathEvent{ID: newID(), X: p.X, Y: p.Y, Z: p.Z, Discovered: o.At, Timestamp: o.At})
		i = len(s.Graves) - 1
	}
	g := &s.Graves[i]
	if g.Player == "" && o.Owner != "" {
		g.Player = o.Owner
		g.OwnerSource = "map"
	}
	// Keep useful identity metadata when a later block has temporarily incomplete metadata.
	if g.Map != nil {
		for key, value := range g.Map.Metadata {
			if o.Metadata == nil {
				o.Metadata = map[string]string{}
			}
			if _, ok := o.Metadata[key]; !ok && key != "owner" && key != "time" {
				o.Metadata[key] = value
			}
		}
	}
	g.Map = &o
	derive(g)
}
