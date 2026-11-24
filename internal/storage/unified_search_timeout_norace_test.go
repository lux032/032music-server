//go:build !race

package storage

import "time"

const (
	largeSearchQueryTimeout  = 5 * time.Second
	largeSearchExactBudget   = 1 * time.Second
	largeSearchNoMatchBudget = 500 * time.Millisecond
	largeSearchBroadBudget   = 2500 * time.Millisecond
)
