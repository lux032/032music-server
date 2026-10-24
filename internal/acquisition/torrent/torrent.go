// Package torrent hashes the exact original bencoded info dictionary.
package torrent

import (
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"strconv"
)

const MaxSize = 5 * 1024 * 1024

var ErrPureV2 = errors.New("不支持纯 v2 种子")
var errInvalid = errors.New("invalid bencoded torrent")

type Metadata struct {
	InfoHash, Name string
	Private        bool
}
type value struct {
	text    string
	integer int64
	kind    byte
	dict    map[string]value
}
type scanner struct {
	b                  []byte
	pos                int
	infoStart, infoEnd int
}

func Parse(b []byte) (Metadata, error) {
	if len(b) == 0 || len(b) > MaxSize {
		return Metadata{}, errInvalid
	}
	p := scanner{b: b}
	v, e := p.scan(0)
	if e != nil || p.pos != len(b) || v.kind != 'd' || p.infoEnd == 0 {
		return Metadata{}, errInvalid
	}
	info := v.dict["info"]
	if info.kind != 'd' {
		return Metadata{}, errInvalid
	}
	if info.dict["meta version"].integer == 2 {
		if _, ok := info.dict["pieces"]; !ok {
			return Metadata{}, ErrPureV2
		}
	}
	h := sha1.Sum(b[p.infoStart:p.infoEnd])
	return Metadata{hex.EncodeToString(h[:]), info.dict["name"].text, info.dict["private"].integer == 1}, nil
}
func (p *scanner) scan(depth int) (value, error) {
	if depth > 64 || p.pos >= len(p.b) {
		return value{}, errInvalid
	}
	c := p.b[p.pos]
	p.pos++
	switch c {
	case 'i':
		start := p.pos
		for p.pos < len(p.b) && p.b[p.pos] != 'e' {
			p.pos++
		}
		if p.pos == len(p.b) {
			return value{}, errInvalid
		}
		s := string(p.b[start:p.pos])
		p.pos++
		if s == "" || s == "-0" || (len(s) > 1 && s[0] == '0') || (len(s) > 2 && s[:2] == "-0") || s[0] == '+' {
			return value{}, errInvalid
		}
		n, e := strconv.ParseInt(s, 10, 64)
		if e != nil {
			return value{}, errInvalid
		}
		return value{kind: c, integer: n}, nil
	case 'l', 'd':
		v := value{kind: c}
		if c == 'd' {
			v.dict = map[string]value{}
		}
		for p.pos < len(p.b) && p.b[p.pos] != 'e' {
			key := ""
			if c == 'd' {
				k, e := p.scan(depth + 1)
				if e != nil || k.kind != 's' {
					return value{}, errInvalid
				}
				key = k.text
				if _, ok := v.dict[key]; ok {
					return value{}, errInvalid
				}
			}
			start := p.pos
			x, e := p.scan(depth + 1)
			if e != nil {
				return value{}, e
			}
			if c == 'd' {
				v.dict[key] = x
				if depth == 0 && key == "info" {
					p.infoStart = start
					p.infoEnd = p.pos
				}
			}
		}
		if p.pos == len(p.b) {
			return value{}, errInvalid
		}
		p.pos++
		return v, nil
	default:
		if c < '0' || c > '9' {
			return value{}, errInvalid
		}
		start := p.pos - 1
		for p.pos < len(p.b) && p.b[p.pos] >= '0' && p.b[p.pos] <= '9' {
			p.pos++
		}
		if p.pos >= len(p.b) || p.b[p.pos] != ':' {
			return value{}, errInvalid
		}
		s := string(p.b[start:p.pos])
		if len(s) > 1 && s[0] == '0' {
			return value{}, errInvalid
		}
		n, e := strconv.ParseUint(s, 10, 64)
		p.pos++
		if e != nil || n > uint64(len(p.b)-p.pos) {
			return value{}, errInvalid
		}
		text := string(p.b[p.pos : p.pos+int(n)])
		p.pos += int(n)
		return value{kind: 's', text: text}, nil
	}
}
