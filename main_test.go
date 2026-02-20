package main

import "testing"

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
