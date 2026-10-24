package discovery

import (
	"github.com/lux032/032music-server/internal/discovery/mb"
	"time"
)

// ReleaseDate uses the beginning of a partial date; invalid dates are unknown.
func ReleaseDate(raw string) time.Time {
	for _, layout := range []string{"2006-01-02", "2006-01", "2006"} {
		if d, e := time.ParseInLocation(layout, raw, time.Local); e == nil {
			return d
		}
	}
	return time.Time{}
}
func Eligible(rg mb.ReleaseGroup, today time.Time) (string, bool) {
	kind := ""
	if len(rg.SecondaryTypes) == 0 {
		switch rg.PrimaryType {
		case "Album", "EP", "Single":
			kind = rg.PrimaryType
		}
	} else if rg.PrimaryType == "Album" && len(rg.SecondaryTypes) == 1 && rg.SecondaryTypes[0] == "Soundtrack" {
		kind = "OST"
	}
	d := ReleaseDate(rg.FirstReleaseDate)
	y, m, day := today.Date()
	return kind, kind != "" && (d.IsZero() || !d.After(time.Date(y, m, day, 0, 0, 0, 0, today.Location())))
}
