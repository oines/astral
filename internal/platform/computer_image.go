package platform

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"time"
)

// The recipe and its published lock travel with every executable. Container
// creation never builds a recipe or resolves a mutable registry tag.
//
//go:embed computer-image/*
var computerImageFiles embed.FS

type computerImageLock struct {
	SchemaVersion int      `json:"schema_version"`
	Image         string   `json:"image"`
	Tag           string   `json:"tag"`
	Digest        string   `json:"digest"`
	RecipeSHA256  string   `json:"recipe_sha256"`
	Platforms     []string `json:"platforms"`
	SourceSHA     string   `json:"source_sha"`
	BuildRunURL   string   `json:"build_run_url"`
	CreatedAt     string   `json:"created_at"`
}

var imageDigestPattern = regexp.MustCompile(`^sha256:[a-f0-9]{64}$`)

func computerImageReference() (string, error) {
	raw, err := computerImageFiles.ReadFile("computer-image/image-lock.json")
	if err != nil {
		return "", fmt.Errorf("this build has no published Computer environment: %w", err)
	}
	recipe, err := computerImageFiles.ReadFile("computer-image/Dockerfile")
	if err != nil {
		return "", err
	}
	return parseComputerImageLock(raw, recipe)
}

func parseComputerImageLock(raw, recipe []byte) (string, error) {
	var lock computerImageLock
	if err := json.Unmarshal(raw, &lock); err != nil {
		return "", fmt.Errorf("invalid Computer environment lock: %w", err)
	}
	hash := sha256.Sum256(recipe)
	recipeHash := hex.EncodeToString(hash[:])
	tag := regexp.MustCompile(`^env-` + recipeHash[:12] + `-([1-9][0-9]*)-[1-9][0-9]*$`).FindStringSubmatch(lock.Tag)
	created, timeErr := time.Parse(time.RFC3339Nano, lock.CreatedAt)
	if lock.SchemaVersion != 1 || lock.Image != "ghcr.io/oines/astral-computer" || !imageDigestPattern.MatchString(lock.Digest) ||
		lock.RecipeSHA256 != recipeHash || len(lock.Platforms) != 2 ||
		!slices.Contains(lock.Platforms, "linux/amd64") || !slices.Contains(lock.Platforms, "linux/arm64") ||
		len(tag) != 2 || !regexp.MustCompile(`^[a-f0-9]{40}$`).MatchString(lock.SourceSHA) ||
		timeErr != nil || created.IsZero() || !strings.HasSuffix(lock.CreatedAt, "Z") {
		return "", fmt.Errorf("the published Computer environment does not match this build's recipe and supported architectures")
	}
	if lock.BuildRunURL != "https://github.com/oines/astral/actions/runs/"+tag[1] {
		return "", fmt.Errorf("the Computer environment lock does not identify its build run")
	}
	return lock.Image + "@" + lock.Digest, nil
}
