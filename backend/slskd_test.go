package backend

import (
	"strings"
	"testing"
)

func TestFoldSlskdDiacritics(t *testing.T) {
	cases := map[string]string{
		"Vasaré":       "Vasare",
		"Ugnė Uma":     "Ugne Uma",
		"Björk":        "Bjork",
		"Møl":          "Mol",
		"Æther":        "AEther",
		"straße":       "strasse",
		"Time Test":    "Time Test",
		"Šarūnas Łódź": "Sarunas Lodz",
	}
	for in, want := range cases {
		if got := foldSlskdDiacritics(in); got != want {
			t.Errorf("foldSlskdDiacritics(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestNormalizeSlskdTextFoldsDiacritics(t *testing.T) {
	// Ranking must match a folded query token against ASCII peer filenames:
	// "Vasaré" and "vasare" normalize identically.
	if got, want := normalizeSlskdText("Vasaré"), "vasare"; got != want {
		t.Errorf("normalizeSlskdText(%q) = %q, want %q", "Vasaré", got, want)
	}
	filename := normalizeSlskdText(`@@abc\music\emer\01-emer_ugne_uma-vasare-e28cdb45.mp3`)
	if !slskdContainsAllTokens(filename, tokenizeSlskdText("Vasaré")) {
		t.Errorf("accented track title should match transliterated filename %q", filename)
	}
}

func TestBuildSlskdSearchLadderFoldsQueries(t *testing.T) {
	ladder := buildSlskdSearchLadder("Vasaré emer, Ugnė Uma", "Vasaré", "emer, Ugnė Uma", "Album")
	if len(ladder) == 0 {
		t.Fatal("empty ladder")
	}
	if ladder[0] != "Vasare emer, Ugne Uma" {
		t.Errorf("first tier should be ASCII-folded, got %q", ladder[0])
	}
	if last := ladder[len(ladder)-1]; last != "Vasaré emer, Ugnė Uma" {
		t.Errorf("last tier should keep the original accented query, got %q", last)
	}
	for _, q := range ladder[:len(ladder)-1] {
		if q != foldSlskdDiacritics(q) {
			t.Errorf("non-final tier %q is not fully folded", q)
		}
	}
}

func TestSlskdAlbumPeerCache(t *testing.T) {
	album, artist := "Test Album Ų", "emer, Ugnė Uma"
	entry := &slskdAlbumPeer{
		Username:  "Grenoybel",
		Directory: `@@abc\music\emer`,
		Files: []SlskdFile{
			{Filename: `@@abc\music\emer\01-emer_ugne_uma-vasare-aa.mp3`, Size: 100},
			{Filename: `@@abc\music\emer\03-emer_ugne_uma-time_test-bb.mp3`, Size: 300},
		},
	}
	slskdStoreAlbumPeer(slskdAlbumKey(album, artist), entry)
	defer slskdForgetAlbumPeer(album, artist, "Grenoybel")

	// Lookup is keyed on normalized text: accents and case must not matter.
	if got := slskdLookupAlbumPeer("test album ų", "EMER, UGNE UMA"); got != entry {
		t.Fatal("album peer cache lookup failed across accent/case variants")
	}

	s := &SlskdDownloader{}
	cands := s.albumFolderCandidates(entry, false, false, 0, "Vasaré", artist, album)
	if len(cands) != 1 {
		t.Fatalf("want exactly 1 folder candidate for %q, got %d", "Vasaré", len(cands))
	}
	if !strings.Contains(cands[0].File.Filename, "vasare") {
		t.Errorf("matched wrong sibling: %q", cands[0].File.Filename)
	}

	slskdForgetAlbumPeer(album, artist, "SomeoneElse")
	if slskdLookupAlbumPeer(album, artist) == nil {
		t.Error("forget with non-matching username must not evict the entry")
	}
	slskdForgetAlbumPeer(album, artist, "grenoybel")
	if slskdLookupAlbumPeer(album, artist) != nil {
		t.Error("forget with matching username (case-insensitive) must evict the entry")
	}
}

func TestSlskdRemoteDirName(t *testing.T) {
	if got := slskdRemoteDirName(`@@abc\music\emer\01-track.mp3`); got != `@@abc\music\emer` {
		t.Errorf("slskdRemoteDirName = %q", got)
	}
	if got := slskdRemoteDirName("bare-file.mp3"); got != "" {
		t.Errorf("bare filename should yield empty dir, got %q", got)
	}
}
