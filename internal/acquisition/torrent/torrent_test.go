package torrent

import (
	"bytes"
	"errors"
	"testing"
)

func TestParseOriginalInfoHash(t *testing.T) {
	// sha1("d4:name4:test6:pieces0:e"), independently fixed fixture.
	b := []byte("d4:infod4:name4:test6:pieces0:ee")
	got, e := Parse(b)
	if e != nil || got.Name != "test" || got.InfoHash != "0bff18183af51b68eb41eaacef1d09f11e1fd14c" {
		t.Fatalf("%+v %v", got, e)
	}
	hybrid := []byte("d4:infod12:meta versioni2e4:name4:test6:pieces0:7:privatei1eee")
	got, e = Parse(hybrid)
	if e != nil || !got.Private {
		t.Fatalf("hybrid %+v %v", got, e)
	}
	if _, e = Parse([]byte("d4:infod12:meta versioni2e4:name4:testee")); !errors.Is(e, ErrPureV2) {
		t.Fatal(e)
	}
}
func TestParseMalformed(t *testing.T) {
	for _, b := range [][]byte{
		nil, []byte("le"), []byte("de"), []byte("d4:infoi1ee"), []byte("d4:infodeeX"), []byte("d4:infod4:name999:xe"), []byte("d4:infod1:ai01eee"), []byte("d4:infod1:ai-0eee"), []byte("d4:infod1:ai+1eee"), []byte("d4:infod01:a0:ee"), []byte("d4:infod1:a0:1:a0:ee"), []byte("d4:infod1:ai9223372036854775808eee"), append([]byte("d4:info"), bytes.Repeat([]byte("l"), 65)...), bytes.Repeat([]byte("x"), MaxSize+1),
	} {
		if _, e := Parse(b); e == nil {
			t.Fatalf("accepted %q", b)
		}
	}
}

func TestUnsortedInfoUsesOriginalBytes(t *testing.T) {
	m, e := Parse([]byte("d4:infod1:z0:1:a0:ee"))
	if e != nil || m.InfoHash != "74e171abe84748be8d0e7c6450377b8832fd7c7f" {
		t.Fatal(m, e)
	}
}
