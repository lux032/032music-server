package guard

import (
	"path/filepath"
	"strings"
	"testing"
)

// 用合成路径覆盖路径拒绝逻辑：.local/data 本体、大小写变体、相对路径
// 绕行、与 source 相同（含大小写变体），以及一个应当放行的正常副本路径。
func TestCheckCopyDBPath(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "work")
	source := filepath.Join(root, ".local", "data", "music.db")
	cases := []struct {
		name    string
		dbPath  string
		source  string
		wantErr bool
	}{
		{"real data dir", filepath.Join(root, ".local", "data", "music.db"), source, true},
		{"case variant of data dir", filepath.Join(root, ".LOCAL", "DATA", "MUSIC.DB"), source, true},
		{"dotdot traversal into data dir", filepath.Join(root, "x", "..", ".local", "data", "music.db"), source, true},
		{"nested under data dir", filepath.Join(root, ".local", "data", "copy", "music.db"), source, true},
		{"same as source exact", filepath.Join(root, ".local", "tmp", "music.db"), filepath.Join(root, ".local", "tmp", "music.db"), true},
		{"same as source case variant", filepath.Join(root, ".local", "tmp", "MUSIC.DB"), filepath.Join(root, ".local", "tmp", "music.db"), true},
		{"same as source via dotdot", filepath.Join(root, ".local", "data", "..", "tmp", "music.db"), filepath.Join(root, ".local", "tmp", "music.db"), true},
		{"legit copy", filepath.Join(root, ".local", "tmp", "rehearsal", "music.db"), source, false},
		{"legit copy without source", filepath.Join(root, ".local", "tmp", "rehearsal", "music.db"), "", false},
		{"data dir as directory prefix only", filepath.Join(root, ".local", "database", "music.db"), source, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := CheckCopyDBPath(tc.dbPath, tc.source)
			if tc.wantErr && err == nil {
				t.Fatalf("CheckCopyDBPath(%q, %q) = nil, want error", tc.dbPath, tc.source)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("CheckCopyDBPath(%q, %q) = %v, want nil", tc.dbPath, tc.source, err)
			}
		})
	}
}

// "data dir as directory prefix only" 用例的边界：`.local\database` 不应被
// 误判为在 .local\data 下（防止 contains 误伤相似前缀目录）。
func TestIsUnderDataDirPrefixBoundaries(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "work")
	under := []string{
		filepath.Join(root, ".local", "data"),
		filepath.Join(root, ".local", "data", "music.db"),
		filepath.Join(root, ".LOCAL", "DATA", "x", "y.db"),
	}
	for _, p := range under {
		if !IsUnderDataDir(p) {
			t.Fatalf("IsUnderDataDir(%q) = false, want true", p)
		}
	}
	outside := []string{
		filepath.Join(root, ".local", "database", "music.db"),
		filepath.Join(root, ".local", "tmp", "music.db"),
	}
	for _, p := range outside {
		if IsUnderDataDir(p) {
			t.Fatalf("IsUnderDataDir(%q) = true, want false", p)
		}
	}
	// Normal 必须统一斜杠与大小写。
	if !strings.Contains(Normal(filepath.Join(root, ".LoCaL", "DATA")), `\.local\data`) {
		t.Fatalf("Normal did not fold case/separators")
	}
}
