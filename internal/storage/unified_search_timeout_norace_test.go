//go:build !race

package storage

import "time"

const largeSearchQueryTimeout = 5 * time.Second
