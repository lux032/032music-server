//go:build ignore

// seed_avatar_batch_e2e.go seeds isolated pending audit_review avatar candidates
// for tests/e2e/avatar-batch-actions.spec.js without touching shared fixtures.
package main

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"

	"github.com/lux032/032music-server/internal/metadata"
	"github.com/lux032/032music-server/internal/storage"
)

func makePNG(r, g, b uint8) []byte {
	img := image.NewRGBA(image.Rect(0, 0, 200, 200))
	c := color.RGBA{R: r, G: g, B: b, A: 255}
	for y := 0; y < 200; y++ {
		for x := 0; x < 200; x++ {
			img.SetRGBA(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		panic(err)
	}
	return buf.Bytes()
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "seed_avatar_batch_e2e: "+format+"\n", args...)
	os.Exit(1)
}

func main() {
	if len(os.Args) < 3 {
		fail("usage: seed_avatar_batch_e2e.go <db-path> <data-dir>")
	}
	dbPath := os.Args[1]
	dataDir := os.Args[2]
	ctx := context.Background()

	s, err := storage.Open(dbPath)
	if err != nil {
		fail("open db: %v", err)
	}
	defer s.Close()

	// Mark first-run setup wizard completed so admin login lands on /admin directly.
	if err := s.MarkSetupWizardCompleted(ctx); err != nil {
		fail("mark setup wizard completed: %v", err)
	}

	// Keep external artist image sources disabled so the isolated E2E server stays offline.
	if err := s.SaveArtistImageSettings(ctx, storage.ArtistImageSettings{
		NegativeCacheDays:   30,
		AuditAutoConfidence: 85,
	}); err != nil {
		fail("save artist image settings: %v", err)
	}

	if err := s.EnsureLibrary(ctx, "AvatarE2ELib", "/avatar-e2e"); err != nil {
		fail("ensure library: %v", err)
	}
	lib, err := s.LibraryByRoot(ctx, "/avatar-e2e")
	if err != nil {
		fail("library by root: %v", err)
	}

	imgDir := filepath.Join(dataDir, "e2e-avatars")
	if err := os.MkdirAll(imgDir, 0o700); err != nil {
		fail("mkdir img dir: %v", err)
	}

	specs := []struct {
		rel   string
		name  string
		hash  string
		state string
		note  string
		r, g, b uint8
	}{
		{"a/1.flac", "AvatarClearOne", "e2eavatarhash01", "not_found_auto", "全量审查已自动联网找图，未找到合适新图", 180, 60, 60},
		{"a/2.flac", "AvatarClearTwo", "e2eavatarhash02", "not_found_auto", "全量审查已自动联网找图，未找到合适新图", 180, 100, 60},
		{"a/3.flac", "AvatarKeepOne", "e2eavatarhash03", "", "", 60, 140, 180},
		{"a/4.flac", "AvatarKeepTwo", "e2eavatarhash04", "", "", 60, 180, 120},
		{"a/5.flac", "AvatarSearchOne", "e2eavatarhash05", "", "", 120, 80, 200},
		{"a/6.flac", "AvatarPollExtra", "e2eavatarhash06", "not_found_manual", "手动联网找图未找到更合适的候选头像", 150, 150, 90},
	}

	for _, sp := range specs {
		if err := s.ImportTrack(ctx, storage.ImportInput{
			LibraryID:    lib.ID,
			RelativePath: sp.rel,
			FileSize:     100,
			ModifiedAtNS: 1,
			Metadata: metadata.AudioMetadata{
				Title:        "Track " + sp.name,
				Album:        "Album " + sp.name,
				Artists:      []string{sp.name},
				AlbumArtists: []string{sp.name},
				DiscNumber:   1,
				TrackNumber:  1,
			},
		}); err != nil {
			fail("import %s: %v", sp.name, err)
		}
	}

	artists, err := s.ArtistsForMatching(ctx)
	if err != nil {
		fail("list artists: %v", err)
	}
	idByName := map[string]int64{}
	for _, a := range artists {
		idByName[a.Name] = a.ID
	}

	for _, sp := range specs {
		aid := idByName[sp.name]
		if aid == 0 {
			fail("missing artist id for %s", sp.name)
		}
		imgPath := filepath.Join(imgDir, sp.hash+".png")
		data := makePNG(sp.r, sp.g, sp.b)
		if err := os.WriteFile(imgPath, data, 0o600); err != nil {
			fail("write png %s: %v", sp.name, err)
		}
		if err := s.SaveArtistImage(ctx, storage.ArtistImageInput{
			ArtistID:  aid,
			Source:    "deezer",
			RemoteURL: "https://img.example/" + sp.hash + ".png",
			Hash:      sp.hash,
			MIMEType:  "image/png",
			CachePath: imgPath,
			ByteSize:  int64(len(data)),
		}); err != nil {
			fail("save artist image %s: %v", sp.name, err)
		}
		if _, err := s.UpsertArtistImageCandidate(ctx, storage.ArtistImageCandidate{
			ArtistID:        aid,
			Source:          "ai_audit",
			ImageURL:        "https://img.example/" + sp.hash + ".png",
			Confidence:      "high",
			ConfidenceScore: 95,
			Width:           200,
			Height:          200,
			Kind:            "audit_review",
			IssueType:       "album_cover",
			Reason:          "测试专辑封面：" + sp.name,
			WebSearchState:  sp.state,
			WebSearchNote:   sp.note,
		}); err != nil {
			fail("upsert candidate %s: %v", sp.name, err)
		}
	}
}
