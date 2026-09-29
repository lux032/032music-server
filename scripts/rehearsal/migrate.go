//go:build ignore

// migrate.go 用项目自身的 storage.Migrate 升级一份**副本**数据库。
// 用法: go run ./scripts/rehearsal/migrate.go <副本 music.db 路径>
// 绝不接受 .local/data 下的路径（真实库）。
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/lux032/032music-server/internal/storage"
	"github.com/lux032/032music-server/scripts/rehearsal/guard"
)

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: go run ./scripts/rehearsal/migrate.go <copy-of-music.db>")
		os.Exit(2)
	}
	dbPath := os.Args[1]
	// H2：Abs+EvalSymlinks 后拒绝 .local/data 与 source 本身（REHEARSAL_SOURCE
	// 由 rehearsal.sh 传入）。
	if err := guard.CheckCopyDBPath(dbPath, os.Getenv("REHEARSAL_SOURCE")); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	store, err := storage.Open(dbPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer store.Close()
	if err := store.Migrate(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, "migrate:", err)
		os.Exit(1)
	}
	fmt.Println("migrate OK:", dbPath)
}
