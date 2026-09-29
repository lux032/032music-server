//go:build ignore

// dbquery.go 以只读方式对 SQLite 数据库执行一条查询并打印结果。
// 用法: go run ./scripts/rehearsal/dbquery.go <db 路径> <SQL>
// 连接串带 query_only(1)，保证不会写入；脚本也不会触碰文件 mtime。
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
		fmt.Fprintln(os.Stderr, "usage: go run ./scripts/rehearsal/dbquery.go <db> <sql>")
		os.Exit(2)
	}
	// H2：只读查询同样拒绝指向真实库的路径（Abs+EvalSymlinks 后判定）。
	if err := guard.CheckCopyDBPath(os.Args[1], os.Getenv("REHEARSAL_SOURCE")); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	db, err := sql.Open("sqlite", os.Args[1]+"?_pragma=busy_timeout(5000)&_pragma=query_only(1)")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer db.Close()
	rows, err := db.Query(os.Args[2])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		for i, v := range vals {
			if i > 0 {
				fmt.Print(" | ")
			}
			fmt.Printf("%v", v)
		}
		fmt.Println()
	}
	if err := rows.Err(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
