package main

import (
	"encoding/json"
	"testing"
)

func TestHasExactVersionOperator(t *testing.T) {
	cases := []struct {
		ref  string
		want bool
	}{
		{"https://flakehub.com/f/Org/proj/%3D3.21.9", true},
		{"https://example.com/f/Org/proj/=1.0.0", true},
		{"https://flakehub.com/f/Org/proj/*", false},
		{"https://flakehub.com/f/Org/proj/3", false},
		{"github:NixOS/nixpkgs/nixos-unstable", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := hasExactVersionOperator(tc.ref); got != tc.want {
			t.Errorf("hasExactVersionOperator(%q)=%v want %v", tc.ref, got, tc.want)
		}
	}
}

func TestFloatingTipRef(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{
			"https://flakehub.com/f/Org/proj/%3D3.21.9",
			"https://flakehub.com/f/Org/proj/*",
		},
		{
			"https://example.com/f/Org/proj/=1.2.3",
			"https://example.com/f/Org/proj/*",
		},
		{
			"https://example.com/f/Org/proj/*",
			"https://example.com/f/Org/proj/*",
		},
	}
	for _, tc := range cases {
		if got := floatingTipRef(tc.in); got != tc.want {
			t.Errorf("floatingTipRef(%q)=%q want %q", tc.in, got, tc.want)
		}
	}
}

func TestVersionFromURLExactPin(t *testing.T) {
	u := "https://flakehub.com/f/Org/proj/%3D3.21.9"
	if got := versionFromURL(u); got != "3.21.9" {
		t.Errorf("versionFromURL pin segment = %q want 3.21.9", got)
	}
	u2 := "https://api.example.com/f/pinned/Org/proj/3.22.0/uuid/source.tar.gz"
	if got := versionFromURL(u2); got != "3.22.0" {
		t.Errorf("versionFromURL archive = %q want 3.22.0", got)
	}
}

func TestParseBehindDetail(t *testing.T) {
	locked := map[string]any{
		"rev":          "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"lastModified": float64(1712448000), // 2024-04-07
	}
	meta := map[string]any{
		"revision":     "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"lastModified": float64(1723939200), // 2024-08-18
		"locked": map[string]any{
			"rev": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		},
	}
	got := parseBehindDetail(locked, meta, locked["rev"].(string), meta["revision"].(string))
	want := "aaaaaaaa (2024-04-07) -> bbbbbbbb (2024-08-18)"
	if got.String() != want {
		t.Fatalf("both dates: %q want %q", got.String(), want)
	}
	if got.Have != "aaaaaaaa" || got.Tip != "bbbbbbbb" || got.HaveDay != "2024-04-07" || got.TipDay != "2024-08-18" {
		t.Fatalf("fields: %+v", got)
	}

	nested := map[string]any{
		"locked": map[string]any{
			"rev":          "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
			"lastModified": float64(1723939200),
		},
	}
	got = parseBehindDetail(locked, nested, locked["rev"].(string), "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	if got.String() != want {
		t.Fatalf("nested tip date: %q want %q", got.String(), want)
	}

	lockOnly := parseBehindDetail(locked, map[string]any{"revision": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"}, locked["rev"].(string), "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb")
	if lockOnly.String() != "aaaaaaaa (2024-04-07) -> bbbbbbbb" {
		t.Fatalf("lock only: %q", lockOnly.String())
	}

	tipOnly := parseBehindDetail(
		map[string]any{"rev": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		meta,
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		meta["revision"].(string),
	)
	if tipOnly.String() != "aaaaaaaa -> bbbbbbbb (2024-08-18)" {
		t.Fatalf("tip only: %q", tipOnly.String())
	}

	neither := parseBehindDetail(
		map[string]any{"rev": "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		map[string]any{"revision": "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"},
		"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
	)
	if neither.String() != "aaaaaaaa -> bbbbbbbb" {
		t.Fatalf("neither: %q", neither.String())
	}

	pinLocked := map[string]any{
		"url":          "https://flakehub.com/f/Org/proj/%3D3.21.9",
		"lastModified": float64(1712448000),
	}
	pinMeta := map[string]any{
		"locked": map[string]any{
			"url":          "https://flakehub.com/f/Org/proj/%3D3.22.1",
			"lastModified": float64(1723939200),
		},
	}
	pin := parseBehindDetail(pinLocked, pinMeta, "old", "new")
	if pin.String() != "3.21.9 (2024-04-07) -> 3.22.1 (2024-08-18)" {
		t.Fatalf("pin versions: %q", pin.String())
	}
}

func TestLastModifiedUnix(t *testing.T) {
	if _, ok := lastModifiedUnix(nil); ok {
		t.Fatal("nil map")
	}
	if _, ok := lastModifiedUnix(map[string]any{}); ok {
		t.Fatal("missing key")
	}
	if _, ok := lastModifiedUnix(map[string]any{"lastModified": float64(0)}); ok {
		t.Fatal("zero")
	}
	got, ok := lastModifiedUnix(map[string]any{"lastModified": float64(1712448000)})
	if !ok || got != 1712448000 {
		t.Fatalf("float64: %d %v", got, ok)
	}
	got, ok = lastModifiedUnix(map[string]any{"lastModified": json.Number("1712448000")})
	if !ok || got != 1712448000 {
		t.Fatalf("json.Number: %d %v", got, ok)
	}
}

func TestIsExactVersionPinOriginal(t *testing.T) {
	orig := map[string]any{
		"type": "tarball",
		"url":  "https://flakehub.com/f/DeterminateSystems/determinate/%3D3.21.9",
	}
	if !isExactVersionPin(orig) {
		t.Fatal("expected exact pin")
	}
	float := map[string]any{
		"type": "tarball",
		"url":  "https://flakehub.com/f/DeterminateSystems/determinate/*",
	}
	if isExactVersionPin(float) {
		t.Fatal("floating should not be exact pin")
	}
}
