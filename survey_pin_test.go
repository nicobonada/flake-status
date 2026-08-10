package main

import "testing"

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
