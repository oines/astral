package platform

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
)

func TestComputerEnvironmentLockBindsRecipeAndArchitectures(t *testing.T) {
	recipe := []byte("FROM ubuntu:24.04\n")
	hash := sha256.Sum256(recipe)
	checksum := hex.EncodeToString(hash[:])
	fixture := computerImageLock{SchemaVersion: 1, Image: "ghcr.io/oines/astral-computer", Tag: "env-" + checksum[:12] + "-123-1", Digest: "sha256:" + strings.Repeat("a", 64), RecipeSHA256: checksum, Platforms: []string{"linux/amd64", "linux/arm64"}, SourceSHA: strings.Repeat("b", 40), BuildRunURL: "https://github.com/oines/astral/actions/runs/123", CreatedAt: "2026-10-27T12:00:00.000Z"}
	encode := func(value computerImageLock) []byte {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	if got, err := parseComputerImageLock(encode(fixture), recipe); err != nil || got != fixture.Image+"@"+fixture.Digest {
		t.Fatalf("valid immutable image: %q, %v", got, err)
	}
	for _, test := range []struct {
		name   string
		change func(*computerImageLock)
	}{
		{"untrusted registry", func(v *computerImageLock) { v.Image = "attacker.invalid/image" }},
		{"mutable tag", func(v *computerImageLock) { v.Digest = "latest" }},
		{"different recipe", func(v *computerImageLock) { v.RecipeSHA256 = strings.Repeat("c", 64) }},
		{"missing arm64", func(v *computerImageLock) { v.Platforms = []string{"linux/amd64"} }},
		{"duplicate architecture", func(v *computerImageLock) { v.Platforms = []string{"linux/amd64", "linux/amd64"} }},
		{"unrelated source", func(v *computerImageLock) { v.SourceSHA = "main" }},
		{"unrelated run", func(v *computerImageLock) { v.BuildRunURL += "4" }},
		{"missing build time", func(v *computerImageLock) { v.CreatedAt = "" }},
		{"unknown schema", func(v *computerImageLock) { v.SchemaVersion++ }},
	} {
		t.Run(test.name, func(t *testing.T) {
			value := fixture
			test.change(&value)
			if _, err := parseComputerImageLock(encode(value), recipe); err == nil {
				t.Fatal("accepted invalid environment lock")
			}
		})
	}
}
