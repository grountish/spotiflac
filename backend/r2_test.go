package backend

import (
	"os"
	"path/filepath"
	"testing"
)

func TestR2QuotaBytes(t *testing.T) {
	cases := []struct {
		name     string
		settings map[string]interface{}
		want     int64
	}{
		{"absent falls back to free tier", map[string]interface{}{}, defaultR2QuotaBytes},
		{"configured GB", map[string]interface{}{"r2QuotaGB": float64(25)}, 25 * 1000 * 1000 * 1000},
		{"fractional GB", map[string]interface{}{"r2QuotaGB": 0.5}, 500 * 1000 * 1000},
		{"zero disables", map[string]interface{}{"r2QuotaGB": float64(0)}, 0},
		{"negative disables", map[string]interface{}{"r2QuotaGB": float64(-5)}, 0},
		{"wrong type disables", map[string]interface{}{"r2QuotaGB": "10"}, 0},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := r2QuotaBytes(tc.settings); got != tc.want {
				t.Fatalf("r2QuotaBytes() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestR2EnforceQuota(t *testing.T) {
	if !r2EnforceQuota(map[string]interface{}{}) {
		t.Fatal("enforcement should default to on when unset")
	}
	if r2EnforceQuota(map[string]interface{}{"r2EnforceQuota": false}) {
		t.Fatal("enforcement should be off when explicitly disabled")
	}
	if !r2EnforceQuota(map[string]interface{}{"r2EnforceQuota": true}) {
		t.Fatal("enforcement should be on when explicitly enabled")
	}
}

func TestR2QuotaMath(t *testing.T) {
	free, percent := r2QuotaMath(2_500_000_000, defaultR2QuotaBytes)
	if free != 7_500_000_000 {
		t.Fatalf("free = %d, want 7500000000", free)
	}
	if percent != 25 {
		t.Fatalf("percent = %v, want 25", percent)
	}

	// Over quota must clamp free space at zero rather than going negative.
	free, percent = r2QuotaMath(12_000_000_000, defaultR2QuotaBytes)
	if free != 0 {
		t.Fatalf("free = %d, want 0 when over quota", free)
	}
	if percent != 120 {
		t.Fatalf("percent = %v, want 120", percent)
	}

	// An unlimited quota reports no headroom figure instead of dividing by zero.
	if free, percent = r2QuotaMath(5_000_000_000, 0); free != 0 || percent != 0 {
		t.Fatalf("unlimited quota = (%d, %v), want (0, 0)", free, percent)
	}
}

func TestHumanBytes(t *testing.T) {
	cases := map[int64]string{
		512:            "512 B",
		1_500:          "1.50 kB",
		2_400_000:      "2.40 MB",
		9_870_000_000:  "9.87 GB",
		10_000_000_000: "10.00 GB",
	}
	for in, want := range cases {
		if got := humanBytes(in); got != want {
			t.Fatalf("humanBytes(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestCollectUploadFiles(t *testing.T) {
	root := t.TempDir()

	album := filepath.Join(root, "Album")
	if err := os.MkdirAll(filepath.Join(album, "disc1"), 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(path string, size int) {
		if err := os.WriteFile(path, make([]byte, size), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(album, "01.flac"), 100)
	write(filepath.Join(album, "disc1", "02.flac"), 250)

	loose := filepath.Join(root, "cover.jpg")
	write(loose, 40)

	files, total := collectUploadFiles([]string{album, loose, filepath.Join(root, "missing")})

	if total != 390 {
		t.Fatalf("total = %d, want 390", total)
	}
	if len(files) != 3 {
		t.Fatalf("len(files) = %d, want 3", len(files))
	}

	keys := map[string]int64{}
	for _, f := range files {
		keys[f.key] = f.size
	}
	// Directory uploads keep the folder name as a prefix; loose files do not.
	if keys["Album/01.flac"] != 100 {
		t.Fatalf("missing Album/01.flac, got keys %v", keys)
	}
	if keys["Album/disc1/02.flac"] != 250 {
		t.Fatalf("missing nested key, got keys %v", keys)
	}
	if keys["cover.jpg"] != 40 {
		t.Fatalf("missing loose file key, got keys %v", keys)
	}
}
