package main

import (
	"regexp"
	"strconv"
	"time"
)

var (
	deathLinePattern = regexp.MustCompile(`^([0-9]{4}-[0-9]{2}-[0-9]{2} [0-9]{2}:[0-9]{2}:[0-9]{2}): ACTION\[Server\]: ([^ ]+) dies at \((-?[0-9]+),(-?[0-9]+),(-?[0-9]+)\)\. Bones placed$`)
	// The bones mod removes emptied graves directly, so it needs an explicit
	// collection log; engine dig logs do not confirm collection.
	collectionLinePattern = regexp.MustCompile(`^([0-9]{4}-[0-9]{2}-[0-9]{2} [0-9]{2}:[0-9]{2}:[0-9]{2}): ACTION\[Server\]: ([^ ]+) collects bones:bones at \((-?[0-9]+),(-?[0-9]+),(-?[0-9]+)\)$`)
)

func parseDeathEvent(line string) (DeathEvent, bool) {
	match := deathLinePattern.FindStringSubmatch(line)
	if len(match) != 6 {
		return DeathEvent{}, false
	}

	timestamp, err := time.ParseInLocation("2006-01-02 15:04:05", match[1], time.Local)
	if err != nil {
		return DeathEvent{}, false
	}

	x, err := strconv.Atoi(match[3])
	if err != nil {
		return DeathEvent{}, false
	}
	y, err := strconv.Atoi(match[4])
	if err != nil {
		return DeathEvent{}, false
	}
	z, err := strconv.Atoi(match[5])
	if err != nil {
		return DeathEvent{}, false
	}

	return DeathEvent{
		Timestamp:  timestamp,
		Player:     match[2],
		X:          x,
		Y:          y,
		Z:          z,
		RawLine:    line,
		Discovered: time.Now(),
	}, true
}

func parseCollectionEvent(line string) (collectionEvent, bool) {
	match := collectionLinePattern.FindStringSubmatch(line)
	if len(match) != 6 {
		return collectionEvent{}, false
	}

	timestamp, err := time.ParseInLocation("2006-01-02 15:04:05", match[1], time.Local)
	if err != nil {
		return collectionEvent{}, false
	}
	x, err := strconv.Atoi(match[3])
	if err != nil {
		return collectionEvent{}, false
	}
	y, err := strconv.Atoi(match[4])
	if err != nil {
		return collectionEvent{}, false
	}
	z, err := strconv.Atoi(match[5])
	if err != nil {
		return collectionEvent{}, false
	}

	return collectionEvent{Timestamp: timestamp, Player: match[2], X: x, Y: y, Z: z}, true
}

func markCollected(deaths []DeathEvent, collection collectionEvent) bool {
	match := -1
	for i := range deaths {
		death := &deaths[i]
		if death.Collected || death.X != collection.X || death.Y != collection.Y || death.Z != collection.Z || death.Timestamp.After(collection.Timestamp) {
			continue
		}
		// Log timestamps have second precision; the later entry wins a tie.
		if match == -1 || !death.Timestamp.Before(deaths[match].Timestamp) {
			match = i
		}
	}
	if match == -1 {
		return false
	}
	deaths[match].Collected = true
	collectedAt := collection.Timestamp
	deaths[match].CollectedAt = &collectedAt
	deaths[match].CollectedBy = collection.Player
	return true
}
