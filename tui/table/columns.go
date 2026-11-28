package table

import (
	"slices"
	"strings"

	"github.com/loderunner/logi/ingest"
)

// Candidate keys for the leading default columns, by priority. A key matches
// without regard to case.
var (
	levelKeys = []string{
		"level", "lvl", "severity", "loglevel", "log_level", "levelname",
	}
	messageKeys = []string{"msg", "message", "text", "log", "event"}
)

// defaultKeys returns the keys of the columns after the timestamp: the level
// key and the message key, when found among known, then every other known key
// in order of appearance. The raw line isn't a column, because the table shows
// it across a row with no value in any column. Neither is timeKey, the field
// the timestamp came from, which would repeat the first column. When no other
// key is known, the raw line is the only column.
func defaultKeys(known []string, timeKey string) []string {
	known = slices.DeleteFunc(slices.Clone(known), func(key string) bool {
		return key == ingest.RawKey || (timeKey != "" && key == timeKey)
	})
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

// messageIndex returns the index in keys of the message key, or noColumn.
func messageIndex(keys []string) int {
	return keyIndex(keys, messageKeys)
}

// levelIndex returns the index in keys of the level key, or noColumn.
func levelIndex(keys []string) int {
	return keyIndex(keys, levelKeys)
}

// keyIndex returns the index in keys of the key that matches the earliest
// candidate, or noColumn.
func keyIndex(keys, candidates []string) int {
	key, ok := findKey(keys, candidates)
	if !ok {
		return noColumn
	}

	return slices.Index(keys, key)
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
