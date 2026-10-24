package enrichment

import (
	"context"
	"time"

	"github.com/lux032/032music-server/internal/sourcegate"
)

func (m *Manager) setBangumiIntervalForTest(d time.Duration) {
	m.bangumiPace = d
	if m.gate == nil {
		m.gate = sourcegate.New(sourcegate.Options{Intervals: map[string]time.Duration{"musicbrainz": 0, "bangumi": d}, Now: m.clockNow, Sleep: func(c context.Context, d time.Duration) error {
			if m.sleep != nil {
				return m.sleep(c, d)
			}
			return sleepContext(c, d)
		}})
	} else {
		m.gate.SetInterval("bangumi", d)
	}
}
