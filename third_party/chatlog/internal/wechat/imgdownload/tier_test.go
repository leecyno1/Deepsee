package imgdownload

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTierOfName(t *testing.T) {
	cases := map[string]int{
		"abc_t.dat":     TierThumb,
		"abc_h.dat":     TierHD,
		"abc_b.dat":     TierBig,
		"abc.dat":       TierMid,
		"abc_hd.dat":    TierHD,
		"abc.thumb.dat": TierThumb,
		"nope.jpg":      TierNone,
	}
	for name, want := range cases {
		if got := TierOfName(name); got != want {
			t.Fatalf("%s: got %d want %d", name, got, want)
		}
	}
}

func TestIsThumbnailNameIncludesConvertedFiles(t *testing.T) {
	for _, name := range []string{"abc_t.dat", "abc_t.jpg", "abc.thumb.webp", "/tmp/abc_thumb.png"} {
		if !IsThumbnailName(name) {
			t.Fatalf("expected thumbnail name: %s", name)
		}
	}
	for _, name := range []string{"abc.dat", "abc.jpg", "abc_b.jpg", "content.thumbnail.jpg"} {
		if IsThumbnailName(name) {
			t.Fatalf("unexpected thumbnail name: %s", name)
		}
	}
}

func TestCollectAndBest(t *testing.T) {
	dir := t.TempDir()
	img := filepath.Join(dir, "msg", "attach", "talker", "2026-07", "Img")
	if err := os.MkdirAll(img, 0o755); err != nil {
		t.Fatal(err)
	}
	md5 := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if err := os.WriteFile(filepath.Join(img, md5+"_t.dat"), []byte("thumb"), 0o600); err != nil {
		t.Fatal(err)
	}
	best, ok := ScanBest(dir, md5, "")
	if !ok || best.Tier != TierThumb {
		t.Fatalf("expected thumb, got ok=%v tier=%d path=%s", ok, best.Tier, best.Path)
	}
	if err := os.WriteFile(filepath.Join(img, md5+"_h.dat"), []byte("hd-image-bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	best, ok = ScanBest(dir, md5, "")
	if !ok || best.Tier != TierHD {
		t.Fatalf("expected hd, got ok=%v tier=%d", ok, best.Tier)
	}
	if !PreferableForOCR(best.Tier) {
		t.Fatal("hd should be preferable for OCR")
	}
}

func TestCollectCandidatesSkipsWildcardFallbackWhenDirectPathExists(t *testing.T) {
	dir := t.TempDir()
	md5 := "cccccccccccccccccccccccccccccccc"
	directStem := filepath.Join("msg", "attach", "selected", "2026-08", "Img", "local-path")
	direct := filepath.Join(dir, directStem+"_t.dat")
	if err := os.MkdirAll(filepath.Dir(direct), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(direct, []byte("direct-thumb"), 0o600); err != nil {
		t.Fatal(err)
	}
	other := filepath.Join(dir, "msg", "attach", "other", "2026-08", "Img", md5+"_h.dat")
	if err := os.MkdirAll(filepath.Dir(other), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, []byte("unrelated-hd"), 0o600); err != nil {
		t.Fatal(err)
	}
	candidates := CollectCandidates(dir, md5, directStem)
	if len(candidates) != 1 || candidates[0].Path != direct {
		t.Fatalf("path-backed lookup crossed into wildcard fallback: %#v", candidates)
	}
}

func TestEnsureOriginalLocalOnly(t *testing.T) {
	dir := t.TempDir()
	img := filepath.Join(dir, "msg", "attach", "talker", "2026-07", "Img")
	if err := os.MkdirAll(img, 0o755); err != nil {
		t.Fatal(err)
	}
	md5 := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if err := os.WriteFile(filepath.Join(img, md5+"_t.dat"), []byte("thumb"), 0o600); err != nil {
		t.Fatal(err)
	}
	ensurer := Ensurer{}
	res, err := ensurer.EnsureOriginal(t.Context(), Request{
		DataDir:  dir,
		MediaKey: md5,
		MinTier:  TierMid,
	})
	if err == nil {
		t.Fatal("expected only-thumbnail error")
	}
	if res.Tier != TierThumb {
		t.Fatalf("tier=%d", res.Tier)
	}
	if err := os.WriteFile(filepath.Join(img, md5+".dat"), []byte("mid-image"), 0o600); err != nil {
		t.Fatal(err)
	}
	res, err = ensurer.EnsureOriginal(t.Context(), Request{
		DataDir:  dir,
		MediaKey: md5,
		MinTier:  TierMid,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Tier != TierMid {
		t.Fatalf("tier=%d", res.Tier)
	}
}
