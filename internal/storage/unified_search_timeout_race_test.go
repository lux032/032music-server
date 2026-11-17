//go:build race

package storage

import "time"

// Race instrumentation slows SQLite/Go loops several-fold; keep race coverage
// on the 18k-track search without enforcing the uninstrumented 5s SLA.
const largeSearchQueryTimeout = 30 * time.Second
