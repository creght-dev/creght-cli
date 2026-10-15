package cli

import "testing"

func TestAssetSkip(t *testing.T) {
	s, err := parseAssetSkip("dmg, .EXE ,*_beta.zip", "20MB")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		key  string
		size int64
		skip bool
	}{
		{"site/1/annulo_2026_10_15_macos_x64.dmg", 1, true},
		{"site/1/Setup_Windows.EXE", 1, true},
		{"site/1/app_beta.zip", 1, true},
		{"site/1/app.zip", 1, false},
		{"site/1/hero.png", 30 << 20, true},
		{"site/1/hero.png", 1 << 20, false},
		{"site/1/hero.png", 0, false}, // size unknown: the download enforces the limit
	}
	for _, c := range cases {
		if got := s.reason(c.key, c.size) != ""; got != c.skip {
			t.Errorf("%s (%d): skip=%v, want %v", c.key, c.size, got, c.skip)
		}
	}
	if _, err := parseAssetSkip("", "abc"); err == nil {
		t.Error("expected error for bad size")
	}
	if s, _ := parseAssetSkip("", ""); s.active() {
		t.Error("empty skip must be inactive")
	}
	for in, want := range map[string]int64{"20MB": 20 << 20, "1.5g": 3 << 29, "512k": 512 << 10, "1000": 1000} {
		if got, err := parseByteSize(in); err != nil || got != want {
			t.Errorf("parseByteSize(%q) = %d, %v; want %d", in, got, err, want)
		}
	}
}

func TestReorderFlagsWithSkipFlags(t *testing.T) {
	got := reorderFlags([]string{"p9", "--site_ids", "s1", "--skip-assets", "dmg,exe", "--max-asset-size", "20MB", "--out", "a.zip"})
	want := []string{"--site_ids", "s1", "--skip-assets", "dmg,exe", "--max-asset-size", "20MB", "--out", "a.zip", "p9"}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}
