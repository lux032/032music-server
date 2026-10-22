// backupcheck 只读校验生产数据库副本，不参与服务启动或恢复路径。
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/lux032/032music-server/internal/storage"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	music := flag.String("music-dir", "/music", "当前音乐根目录（只影响差异告警）")
	images := flag.String("images-dir", "", "自定义图片目录（只影响缺失告警）")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "用法: go run ./scripts/backupcheck [-music-dir /music] [-images-dir path] snapshot.db")
		os.Exit(2)
	}
	path, err := filepath.Abs(flag.Arg(0))
	if err != nil {
		return err
	}
	// Validate 会清理其工作副本的 sidecar；先复制到独立临时目录，绝不改动输入。
	dir, err := os.MkdirTemp("", "music-backupcheck-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	target := filepath.Join(dir, "snapshot.db")
	for _, suffix := range []string{"", "-wal", "-shm"} {
		raw, e := os.ReadFile(path + suffix)
		if os.IsNotExist(e) && suffix != "" {
			continue
		}
		if e != nil {
			return e
		}
		if e = os.WriteFile(target+suffix, raw, 0600); e != nil {
			return e
		}
	}
	b := storage.NewBackups(target, filepath.Dir(path), *music)
	if *images != "" {
		b.ImagesDirectory = *images
	}
	v, err := b.Validate(context.Background(), target)
	if err != nil {
		return fmt.Errorf("校验拒绝: %w", err)
	}
	out, _ := json.MarshalIndent(v, "", "  ")
	fmt.Println(string(out))
	return nil
}
