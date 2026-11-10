// Package skills owns Astral's managed skill packages and published revisions.
package skills

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"golang.org/x/text/cases"
	"golang.org/x/text/unicode/norm"
	"gopkg.in/yaml.v3"
)

const (
	MaxArchiveBytes = 16 << 20
	maxEntries      = 2048
	maxSkillMDBytes = 1 << 20
)

var (
	skillName = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)
	foldPath  = cases.Fold()
)

func ValidName(name string) bool {
	return len(name) <= 64 && skillName.MatchString(name) && portablePath(name)
}

func Digest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func validDigest(digest string) bool {
	if len(digest) != sha256.Size*2 || digest != strings.ToLower(digest) {
		return false
	}
	_, err := hex.DecodeString(digest)
	return err == nil
}

func portablePath(name string) bool {
	if len(name) > 240 || !utf8.ValidString(name) || !fs.ValidPath(name) || name == "." {
		return false
	}
	for _, component := range strings.Split(name, "/") {
		if len(component) > 100 || strings.ContainsAny(component, "\\:*?\"<>|") ||
			strings.ContainsFunc(component, unicode.IsControl) || strings.HasSuffix(component, ".") ||
			strings.HasSuffix(component, " ") || excluded(component) {
			return false
		}
		stem, _, _ := strings.Cut(component, ".")
		stem = strings.ToUpper(strings.TrimRight(stem, " "))
		switch stem {
		case "CON", "PRN", "AUX", "NUL", "CONIN$", "CONOUT$":
			return false
		}
		if strings.HasPrefix(stem, "COM") || strings.HasPrefix(stem, "LPT") {
			switch stem[3:] {
			case "1", "2", "3", "4", "5", "6", "7", "8", "9", "¹", "²", "³":
				return false
			}
		}
	}
	return true
}

// Excluded names are ignored both when measuring a checkout and when preparing
// a draft. They are never accepted inside a published archive.
func excluded(name string) bool {
	switch strings.ToLower(name) {
	case ".ds_store", "thumbs.db", "desktop.ini", ".git", "__pycache__", ".pytest_cache", ".mypy_cache", ".ruff_cache":
		return true
	default:
		return false
	}
}

type packEntry struct {
	name   string
	source string
	info   fs.FileInfo
}

type archiveLimit struct {
	w         io.Writer
	remaining int
}

func (w *archiveLimit) Write(p []byte) (int, error) {
	if len(p) > w.remaining {
		return 0, errors.New("managed skill archive exceeds 16 MiB")
	}
	n, err := w.w.Write(p)
	w.remaining -= n
	return n, err
}

// Pack snapshots a directory as an uncompressed, canonical tar. File data and
// empty directories are preserved. Timestamps, ownership and permission bits
// are normalized so the same checkout has the same digest on every Computer.
func Pack(ctx context.Context, dir string) ([]byte, string, error) {
	var out bytes.Buffer
	if err := writeDirectory(ctx, dir, &out, nil); err != nil {
		return nil, "", err
	}
	return out.Bytes(), Digest(out.Bytes()), nil
}

// DirectoryDigest hashes the same tar bytes as Pack without materializing the
// archive. This is used for dirty detection before staging or activation.
func DirectoryDigest(ctx context.Context, dir string) (string, error) {
	return directoryDigest(ctx, dir, nil)
}

// DirectoryDigestForUpdate also reports local Git metadata, which is omitted
// from the published archive but must survive automatic replacement.
func DirectoryDigestForUpdate(ctx context.Context, dir string) (string, bool, error) {
	var hasGit bool
	digest, err := directoryDigest(ctx, dir, &hasGit)
	return digest, hasGit, err
}

func directoryDigest(ctx context.Context, dir string, hasGit *bool) (string, error) {
	hash := sha256.New()
	if err := writeDirectory(ctx, dir, hash, hasGit); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func writeDirectory(ctx context.Context, dir string, out io.Writer, hasGit *bool) error {
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("skill package root must be a directory")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	opened, err := root.Stat(".")
	if err != nil {
		return err
	}
	if !os.SameFile(info, opened) {
		return errors.New("skill package directory changed while packing")
	}
	return writeArchive(ctx, root.FS(), true, out, hasGit)
}

func writeArchive(ctx context.Context, source fs.FS, local bool, out io.Writer, hasGit *bool) error {
	var entries []packEntry
	seen := make(map[string]bool)
	err := fs.WalkDir(source, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if name == "." {
			return nil
		}
		if excluded(entry.Name()) {
			if hasGit != nil && strings.EqualFold(entry.Name(), ".git") {
				*hasGit = true
			}
			if entry.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.IsDir() && !info.Mode().IsRegular() {
			return fmt.Errorf("skill path %q must be a regular file or directory", name)
		}
		canonical := norm.NFC.String(name)
		if !portablePath(canonical) {
			return fmt.Errorf("skill path %q is not portable", name)
		}
		key := foldPath.String(canonical)
		if seen[key] {
			return fmt.Errorf("skill path %q collides with another entry", name)
		}
		seen[key] = true
		if len(entries) == maxEntries {
			return errors.New("managed skill package exceeds 2048 entries")
		}
		entries = append(entries, packEntry{name: canonical, source: name, info: info})
		return nil
	})
	if err != nil {
		return err
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
	tw := tar.NewWriter(&archiveLimit{w: out, remaining: MaxArchiveBytes})
	var buffer [32 << 10]byte
	for _, entry := range entries {
		if err = ctx.Err(); err != nil {
			return err
		}
		if !entry.info.IsDir() && entry.info.Size() > MaxArchiveBytes {
			return fmt.Errorf("skill file %q exceeds 16 MiB", entry.name)
		}
		if err = tw.WriteHeader(archiveHeader(entry.name, entry.info.IsDir(), entry.info.Size())); err != nil {
			return err
		}
		if !entry.info.IsDir() {
			if err = packFile(tw, source, entry, local, buffer[:]); err != nil {
				return err
			}
		}
	}
	return tw.Close()
}

func packFile(w io.Writer, source fs.FS, entry packEntry, local bool, buffer []byte) error {
	file, err := source.Open(entry.source)
	if err != nil {
		return err
	}
	defer file.Close()
	check := func() error {
		info, err := file.Stat()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() != entry.info.Size() ||
			!info.ModTime().Equal(entry.info.ModTime()) || local && !os.SameFile(info, entry.info) {
			return fmt.Errorf("skill file %q changed while packing", entry.name)
		}
		return nil
	}
	if err = check(); err != nil {
		return err
	}
	written, err := io.CopyBuffer(w, io.LimitReader(file, entry.info.Size()), buffer)
	if err != nil {
		return err
	}
	if written != entry.info.Size() {
		return fmt.Errorf("skill file %q changed while packing", entry.name)
	}
	return check()
}

func archiveHeader(name string, dir bool, size int64) *tar.Header {
	h := &tar.Header{Name: name, Mode: 0600, Size: size, Typeflag: tar.TypeReg, Format: tar.FormatPAX}
	if dir {
		h.Name += "/"
		h.Mode, h.Size, h.Typeflag = 0700, 0, tar.TypeDir
	}
	return h
}

type archiveMatch struct{ remaining []byte }

func (m *archiveMatch) Write(p []byte) (int, error) {
	if len(p) > len(m.remaining) || !bytes.Equal(p, m.remaining[:len(p)]) {
		return 0, errors.New("managed skill tar is not in canonical form")
	}
	m.remaining = m.remaining[len(p):]
	return len(p), nil
}

func validateArchive(ctx context.Context, data []byte, sharedName string) error {
	if len(data) > MaxArchiveBytes {
		return errors.New("managed skill archive exceeds 16 MiB")
	}
	match := &archiveMatch{remaining: data}
	tw := tar.NewWriter(match)
	tr := tar.NewReader(bytes.NewReader(data))
	seen := make(map[string]bool)
	dirs := make(map[string]bool)
	previous := ""
	foundSkill := false
	var buffer [32 << 10]byte
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		dir := h.Typeflag == tar.TypeDir
		if !dir && h.Typeflag != tar.TypeReg {
			return fmt.Errorf("unsupported managed skill tar entry %q", h.Name)
		}
		name := h.Name
		if dir {
			name = strings.TrimSuffix(name, "/")
		}
		key := foldPath.String(name)
		if !portablePath(name) || name != norm.NFC.String(name) || name <= previous ||
			seen[key] || len(seen) == maxEntries {
			return fmt.Errorf("invalid or duplicate managed skill tar path %q", h.Name)
		}
		if parent := path.Dir(name); parent != "." && !dirs[parent] {
			return fmt.Errorf("managed skill tar is missing parent directory for %q", h.Name)
		}
		seen[key], dirs[name], previous = true, dir, name
		if err = tw.WriteHeader(archiveHeader(name, dir, h.Size)); err != nil {
			return err
		}
		var content io.Reader = tr
		if sharedName != "" && name == "SKILL.md" {
			if dir || h.Size > maxSkillMDBytes {
				return errors.New("SKILL.md must be a regular file of at most 1 MiB")
			}
			frontmatter, err := io.ReadAll(tr)
			if err != nil {
				return err
			}
			if err = validateSkillMD(frontmatter, sharedName); err != nil {
				return err
			}
			foundSkill = true
			content = bytes.NewReader(frontmatter)
		}
		if _, err = io.CopyBuffer(tw, content, buffer[:]); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if len(match.remaining) != 0 {
		return errors.New("managed skill tar has trailing data")
	}
	if sharedName != "" && !foundSkill {
		return errors.New("shared skill is missing its root SKILL.md")
	}
	return nil
}

// Unpack writes a validated archive into a new or empty staging directory.
// Callers own activation and cleanup on error.
func Unpack(ctx context.Context, data []byte, dir string) error {
	if err := validateArchive(ctx, data, ""); err != nil {
		return err
	}
	if err := os.Mkdir(dir, 0700); err != nil && !errors.Is(err, fs.ErrExist) {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("skill staging root must be a directory")
	}
	root, err := os.OpenRoot(dir)
	if err != nil {
		return err
	}
	defer root.Close()
	opened, err := root.Stat(".")
	if err != nil {
		return err
	}
	if !os.SameFile(info, opened) {
		return errors.New("skill staging directory changed while opening")
	}
	entries, err := fs.ReadDir(root.FS(), ".")
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return errors.New("skill staging directory is not empty")
	}
	tr := tar.NewReader(bytes.NewReader(data))
	var buffer [32 << 10]byte
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		if h.Typeflag == tar.TypeDir {
			if err = root.Mkdir(strings.TrimSuffix(h.Name, "/"), 0700); err != nil {
				return err
			}
			continue
		}
		f, err := root.OpenFile(h.Name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if err != nil {
			return err
		}
		// Hide os.File.ReadFrom so the same copy buffer is reused for every file.
		_, writeErr := io.CopyBuffer(struct{ io.Writer }{f}, tr, buffer[:])
		if err = errors.Join(writeErr, f.Close()); err != nil {
			return err
		}
	}
}

func validateSkillMD(data []byte, name string) error {
	if !utf8.Valid(data) {
		return errors.New("SKILL.md must be UTF-8")
	}
	text := strings.ReplaceAll(strings.TrimPrefix(string(data), "\ufeff"), "\r\n", "\n")
	start, rest, _ := strings.Cut(text, "\n")
	if strings.TrimSpace(start) != "---" {
		return errors.New("SKILL.md needs YAML frontmatter")
	}
	for offset := 0; offset < len(rest); {
		line, _, _ := strings.Cut(rest[offset:], "\n")
		if value := strings.TrimSpace(line); value == "---" || value == "..." {
			var fields struct {
				Name        string `yaml:"name"`
				Description string `yaml:"description"`
			}
			if err := yaml.Unmarshal([]byte(rest[:offset]), &fields); err != nil {
				return errors.New("SKILL.md contains invalid YAML frontmatter")
			}
			if strings.TrimSpace(fields.Name) != name || strings.TrimSpace(fields.Description) == "" {
				return errors.New("SKILL.md needs a matching name and a nonempty description")
			}
			return nil
		}
		offset += len(line) + 1
	}
	return errors.New("SKILL.md frontmatter is not closed")
}
