package tui

import (
	"slices"
	"strings"

	"github.com/loderunner/logi/ingest"
)

// Candidate keys for the leading default columns, by priority. A key matches without
// regard to case.
var (
	levelKeys = []string{
		"level", "lvl", "severity", "loglevel", "log_level", "levelname",
	}
	messageKeys = []string{"msg", "message", "text", "log", "event"}
)

// defaultKeys returns the keys of the columns after the timestamp: the level
// key and the message key, when found among known, then every other known key
// in order of appearance. It returns the key of the raw line when nothing is
// known yet.
func defaultKeys(known []string) []string {
	if len(known) == 0 {
		return []string{ingest.RawKey}
	}

	var keys []string

	if level, ok := findKey(known, levelKeys); ok {
		keys = append(keys, level)
	}

	if message, ok := findKey(known, messageKeys); ok {
		keys = append(keys, message)
	}

	for _, key := range known {
		if !slices.Contains(keys, key) {
			keys = append(keys, key)
		}
	}

	return keys
}

// messageIndex returns the index in keys of the message key, or -1.
func messageIndex(keys []string) int {
	message, ok := findKey(keys, messageKeys)
	if !ok {
		return -1
	}

	return slices.Index(keys, message)
}

// findKey returns the key in known that matches the earliest candidate. When
// several keys match one candidate, such as "Level" and "level", it returns
// the one that appeared first.
func findKey(known, candidates []string) (string, bool) {
	for _, candidate := range candidates {
		for _, key := range known {
			if strings.EqualFold(key, candidate) {
				return key, true
			}
		}
	}

	return "", false
}
