package tui

import (
	"strings"

	"github.com/loderunner/logi/ingest"
)

// Candidate keys for the default columns, by priority. A key matches without
// regard to case.
var (
	levelKeys = []string{
		"level", "lvl", "severity", "loglevel", "log_level", "levelname",
	}
	messageKeys = []string{"msg", "message", "text", "log", "event"}
)

// defaultKeys returns the keys of the default columns after the timestamp:
// the level key then the message key, for those found among known. It returns
// the key of the raw line when neither is found.
func defaultKeys(known []string) []string {
	var keys []string

	level, ok := findKey(known, levelKeys)
	if ok {
		keys = append(keys, level)
	}

	message, ok := findKey(known, messageKeys)
	if ok {
		keys = append(keys, message)
	}

	if len(keys) == 0 {
		return []string{ingest.RawKey}
	}

	return keys
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
