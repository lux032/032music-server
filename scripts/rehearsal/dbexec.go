//go:build ignore

// dbexec.go 对**副本**数据库执行一条写 SQL（例如关闭数据源开关）。
// 用法: go run ./scripts/rehearsal/dbexec.go <副本 db 路径> <SQL>
// 绝不接受 .local\data 下的路径（真实库）。
package main

import (
	"database/sql"
	"fmt"
	"os"

	"github.com/lux032/032music-server/scripts/rehearsal/guard"
	_ "modernc.org/sqlite"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: go run ./scripts/rehearsal/dbexec.go <copy-db> <sql>")
		os.Exit(2)
	}
	dbPath := os.Args[1]
	// H2：Abs+EvalSymlinks 后拒绝 .local/data 与 source 本身（REHEARSAL_SOURCE
	// 由 rehearsal.sh 传入）。
	if err := guard.CheckCopyDBPath(dbPath, os.Getenv("REHEARSAL_SOURCE")); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer db.Close()
	result, err := db.Exec(os.Args[2])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	affected, _ := result.RowsAffected()
	fmt.Printf("exec OK (%d rows): %s\n", affected, os.Args[2])
}
