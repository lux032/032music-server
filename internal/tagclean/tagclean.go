// Package tagclean normalizes raw audio tag values before they are stored or
// displayed. Tag writers may embed NUL separators and other C0 control
// characters (e.g. a catalog number stored as "\x00\x00\x00\x00ARCD0012");
// every tag-derived display field must go through the same cleaning rule
// (D-9/D62/D-19). The package is dependency-free so both the metadata reader
// and the storage layer can share one implementation.
package tagclean

import "strings"

// Value normalizes one raw tag value: split on NUL bytes and take the first
// segment that survives cleaning, remove C0 control characters (tab folds to
// a space), then trim surrounding whitespace. Returns "" when nothing
// survives.
func Value(value string) string {
	for _, segment := range strings.Split(value, "\x00") {
		cleaned := strings.TrimSpace(stripC0Controls(segment))
		if cleaned != "" {
			return cleaned
		}
	}
	return ""
}

// First returns the first value that cleans to non-empty: a first value that
// cleans to empty falls back to the next value of the same list.
func First(values []string) string {
	for _, value := range values {
		if cleaned := Value(value); cleaned != "" {
			return cleaned
		}
	}
	return ""
}

// FirstKey returns the cleaned value of the first key present in raw. When
// the key exists but every value cleans to empty it returns "" without
// consulting later keys — the presence of the key is intentional (mirrors
// the storage-side D62 rule).
func FirstKey(raw map[string][]string, keys ...string) string {
	for _, key := range keys {
		values := raw[key]
		if len(values) == 0 {
			continue
		}
		return First(values)
	}
	return ""
}

func stripC0Controls(value string) string {
	return strings.Map(func(r rune) rune {
		if r == '\t' {
			return ' '
		}
		if r < 0x20 {
			return -1
		}
		return r
	}, value)
}
