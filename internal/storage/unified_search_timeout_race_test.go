//go:build race

package storage

import "time"

// Race instrumentation slows SQLite/Go loops several-fold; keep race coverage
// on the 18k-track search without enforcing the uninstrumented 5s SLA.
const (
	largeSearchQueryTimeout  = 30 * time.Second
	largeSearchExactBudget   = 15 * time.Second
	largeSearchNoMatchBudget = 10 * time.Second
	largeSearchBroadBudget   = 30 * time.Second
)
