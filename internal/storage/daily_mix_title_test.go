package storage

import "testing"

func TestTitleVersion(t *testing.T) {
	for title, want := range map[string]string{
		"祈り、終われば -TV Size-":                    "nonmain",
		"only my railgun (TV ver.)":            "nonmain",
		"紅蓮華 (Instrumental)":                   "nonmain",
		"妄想コントローラー -2015武道館 Live Ichigo Ver.-": "live",
		"Last Surprise (Jazztronik Remix)":     "remix",
		"Blumenkranz : [nZk] ver.":             "remix",
		"夜の東側":                                 "",
		"Live and Learn":                       "",
		"Liver":                                "",
	} {
		if got := titleVersion(title); got != want {
			t.Errorf("%q = %q, want %q", title, got, want)
		}
	}
	if baseTitle("祈り、終われば -TV Size-") != baseTitle("祈り、終われば") || baseTitle("紅蓮華 (Live)") != "紅蓮華" {
		t.Fatalf("base %q %q", baseTitle("祈り、終われば -TV Size-"), baseTitle("紅蓮華 (Live)"))
	}
	if baseTitle("Re:Zero -Starting Life-") == "" {
		t.Fatal("never empty")
	}
}
