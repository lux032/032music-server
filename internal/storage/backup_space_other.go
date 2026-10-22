//go:build !linux && !darwin && !freebsd

package storage

func backupFreeSpace(string) (uint64, error) { return ^uint64(0), nil }
