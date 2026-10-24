// Package bangumi contains shared Bangumi subject vocabulary and name matching helpers.
package bangumi

import (
	"errors"
	"regexp"
	"strings"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
)

var ErrGone = errors.New("bangumi subject gone")

type MusicSubject struct {
	ID                 int64
	Name, NameCN, Date string
	Artists            []string
}

type Relation struct {
	ID       int64  `json:"id"`
	Type     int    `json:"type"`
	Name     string `json:"name"`
	NameCN   string `json:"name_cn"`
	Date     string `json:"date"`
	Platform string `json:"platform"`
	Relation string `json:"relation"`
	Images   struct {
		Large  string `json:"large"`
		Common string `json:"common"`
	} `json:"images"`
}

var artistSeparators = regexp.MustCompile(`(?i)(?:feat\.|ft\.|cv\.|cv:|vo\.)`)

func ArtistTokens(raw string) map[string]bool {
	raw = artistSeparators.ReplaceAllString(cases.Fold().String(norm.NFKC.String(raw)), "/")
	tokens := map[string]bool{}
	for _, part := range strings.FieldsFunc(raw, func(r rune) bool { return strings.ContainsRune("()[]/、,，&×・;", r) }) {
		part = strings.Join(strings.Fields(part), "")
		if part != "" {
			tokens[part] = true
		}
	}
	return tokens
}
func RelationRole(s string) string {
	switch s {
	case "片头曲":
		return "op"
	case "片尾曲":
		return "ed"
	case "插入歌":
		return "insert"
	case "原声集":
		return "ost"
	case "角色歌":
		return "character"
	case "主题歌":
		return "theme"
	case "印象曲", "意象歌":
		return "image_song"
	}
	return "other"
}

// SplitMultiTitle separates an entry that names several songs ("A / B").
// Every slash is a separator, including a single slash inside one title
// ("赤い罠(who loves it?)/ADAMAS"). Fewer than two non-empty parts is not a multi-title.
func SplitMultiTitle(name string) []string {
	name = norm.NFKC.String(strings.TrimSpace(name))
	parts := strings.FieldsFunc(name, func(r rune) bool { return r == '/' })
	var out []string
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	if len(out) < 2 {
		return nil
	}
	return out
}
