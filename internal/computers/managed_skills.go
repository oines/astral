package computers

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/skills"
)

const (
	builtinSkillScope           = "astral-builtin"
	sharedSkillScope            = "astral-shared"
	localSkillDraft             = "local"
	skillGenerations            = ".generations"
	maxSkillState               = 16 << 20
	managedSkillCacheEntries    = 128
	managedSkillCacheEntryBytes = 4 << 10
)

// LocalSkill identifies an editable package by its exact path and base revision.
// ContentDigest describes the files at the time of this status inspection.
type LocalSkill struct {
	Name          string `json:"name"`
	Revision      int64  `json:"revision"`
	Digest        string `json:"digest"`
	ContentDigest string `json:"content_digest,omitempty"`
	Path          string `json:"path"`
	DraftID       string `json:"draft_id"`
	Active        bool   `json:"active"`
	Dirty         bool   `json:"dirty"`
	Error         string `json:"error,omitempty"`
}

type LocalSkillsStatus struct {
	BuiltinRoot string       `json:"builtin_root"`
	SharedRoot  string       `json:"shared_root"`
	Builtin     *LocalSkill  `json:"builtin,omitempty"`
	Shared      []LocalSkill `json:"shared"`
	SyncError   string       `json:"sync_error,omitempty"`
}

type installedBundle struct {
	ID      string         `json:"id,omitempty"` // The fixed path of a legacy installation.
	Version skills.Version `json:"version"`
}

type installedShared struct {
	Active string                    `json:"active"`
	Drafts map[string]skills.Version `json:"drafts"`
}

type installedSkills struct {
	Builtin installedBundle            `json:"builtin"`
	Shared  map[string]installedShared `json:"shared"`
}

type managedSkills struct {
	root          string
	canonicalRoot string
	mu            sync.Mutex   // Serializes durable state changes, never held by discovery.
	builtinMu     sync.RWMutex // Keeps Built-in discovery clear of directory renames.
	state         atomic.Pointer[installedSkills]
	loadErr       error
	syncErr       atomic.Pointer[error]
	metadata      managedSkillCache
}

func newManagedSkills(root string) *managedSkills {
	canonical, err := filepath.EvalSymlinks(root)
	if err != nil {
		canonical = root
	}
	m := &managedSkills{root: filepath.Join(root, "skills"), canonicalRoot: filepath.Join(canonical, "skills")}
	m.state.Store(&installedSkills{})
	m.loadErr = m.load()
	if m.loadErr == nil {
		recorded := m.state.Load()
		state := *recorded
		err := m.recoverBuiltin(&state.Builtin)
		if state.Builtin != recorded.Builtin {
			err = errors.Join(err, m.saveLocked(state))
		}
		m.state.Store(&state)
		m.setSyncError(err)
	}
	return m
}

type managedSkillMetadata struct{ name, description string }

type managedSkillCache struct {
	mu      sync.Mutex
	entries map[string]managedSkillMetadata
	keys    [managedSkillCacheEntries]string
	next    int
}

func (c *managedSkillCache) frontmatter(digest string, data []byte) (string, string, error) {
	c.mu.Lock()
	cached, ok := c.entries[digest]
	c.mu.Unlock()
	if ok {
		return cached.name, cached.description, nil
	}
	name, description, err := skillFrontmatter(data)
	if err != nil || len(name)+len(description) > managedSkillCacheEntryBytes {
		return name, description, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = make(map[string]managedSkillMetadata)
	}
	if _, exists := c.entries[digest]; !exists {
		delete(c.entries, c.keys[c.next])
		c.entries[digest] = managedSkillMetadata{name, description}
		c.keys[c.next] = digest
		c.next = (c.next + 1) % len(c.keys)
	}
	return name, description, nil
}

func (m *managedSkills) builtinRoot() string { return filepath.Join(m.root, builtinSkillScope) }
func (m *managedSkills) sharedRoot() string  { return filepath.Join(m.root, sharedSkillScope) }
func (m *managedSkills) statePath() string   { return filepath.Join(m.root, "state.json") }

func (m *managedSkills) draftPath(name, id string) string {
	if id == localSkillDraft {
		return filepath.Join(m.sharedRoot(), name)
	}
	return filepath.Join(m.sharedRoot(), skillGenerations, id, name)
}

func validSkillGeneration(id string) bool {
	return strings.HasPrefix(id, "generation-") && validIdentity(id)
}

func validInstalledSkill(version skills.Version, name string) bool {
	if version.Name != name || name == "" && version.Revision != 0 || name != "" && version.Revision <= 0 {
		return false
	}
	if len(version.Digest) != 64 || strings.ToLower(version.Digest) != version.Digest {
		return false
	}
	_, err := hex.DecodeString(version.Digest)
	return err == nil
}

// Require ordinary managed directories from the Daemon's data root down. In
// particular, an existing symlink cannot redirect an install into a user root.
func (m *managedSkills) directory(path string, create bool) error {
	base := filepath.Dir(m.root)
	rel, err := filepath.Rel(base, path)
	if err != nil || !filepath.IsLocal(rel) {
		return domain.Invalid("managed skill directory escapes the Daemon data directory")
	}
	for part := range strings.SplitSeq(rel, string(filepath.Separator)) {
		base = filepath.Join(base, part)
		if create {
			if err := os.Mkdir(base, 0700); err != nil && !errors.Is(err, os.ErrExist) {
				return err
			}
		}
		info, err := os.Lstat(base)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return domain.Invalid("managed skill path is not an ordinary directory: " + base)
		}
	}
	return nil
}

func (m *managedSkills) load() error {
	if err := m.directory(m.root, false); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	info, err := os.Lstat(m.statePath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Size() > maxSkillState {
		return errors.New("invalid managed skills state file")
	}
	file, err := os.Open(m.statePath())
	if err != nil {
		return err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maxSkillState+1))
	if err != nil {
		return err
	}
	var state installedSkills
	if len(raw) > maxSkillState || json.Unmarshal(raw, &state) != nil ||
		state.Builtin.ID != "" && !validSkillGeneration(state.Builtin.ID) ||
		state.Builtin != (installedBundle{}) && !validInstalledSkill(state.Builtin.Version, "") {
		return errors.New("invalid managed skills state")
	}
	for name, entry := range state.Shared {
		if !skills.ValidName(name) || entry.Active != "" && entry.Drafts[entry.Active].Revision == 0 {
			return errors.New("invalid shared skill state")
		}
		for id, version := range entry.Drafts {
			if id != localSkillDraft && !validSkillGeneration(id) || !validInstalledSkill(version, name) {
				return errors.New("invalid shared skill generation")
			}
		}
	}
	m.state.Store(&state)
	return nil
}

func (m *managedSkills) saveLocked(state installedSkills) error {
	if err := m.directory(m.root, true); err != nil {
		return err
	}
	raw, err := json.Marshal(state)
	if err != nil {
		return err
	}
	if len(raw) > maxSkillState {
		return errors.New("managed skills state exceeds its size limit")
	}
	file, err := os.CreateTemp(m.root, ".skill-state-")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err = file.Write(raw); err == nil {
		err = file.Sync()
	}
	if err = errors.Join(err, file.Close()); err != nil {
		return err
	}
	if err = moveManagedSkillPath(file.Name(), m.statePath(), true); err != nil {
		return err
	}
	if err = syncManagedSkillDir(m.root); err != nil {
		return err
	}
	m.state.Store(&state)
	return nil
}

func (m *managedSkills) snapshot() (installedSkills, error) {
	return *m.state.Load(), m.loadErr
}

func (m *managedSkills) setSyncError(err error) {
	if err == nil {
		m.syncErr.Store(nil)
	} else {
		m.syncErr.Store(&err)
	}
}

func (m *managedSkills) pack(ctx context.Context, path string) ([]byte, string, error) {
	if err := m.directory(path, false); err != nil {
		return nil, "", err
	}
	return skills.Pack(ctx, path)
}

func (m *managedSkills) contentDigest(ctx context.Context, path string) (string, error) {
	if err := m.directory(path, false); err != nil {
		return "", err
	}
	return skills.DirectoryDigest(ctx, path)
}

func flushManagedSkillTree(ctx context.Context, path string) error {
	root, err := os.OpenRoot(path)
	if err != nil {
		return err
	}
	defer root.Close()
	var directories []string
	err = fs.WalkDir(root.FS(), ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			directories = append(directories, name)
			return nil
		}
		if !entry.Type().IsRegular() {
			return domain.Invalid("invalid file in staged skills")
		}
		file, err := root.OpenFile(name, os.O_RDWR, 0)
		if err != nil {
			return err
		}
		return errors.Join(file.Sync(), file.Close())
	})
	if err != nil {
		return err
	}
	// Children must be durable before the metadata that makes them reachable.
	for i := len(directories) - 1; i >= 0; i-- {
		if err := syncManagedSkillDir(filepath.Join(path, filepath.FromSlash(directories[i]))); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func (m *managedSkills) stageSkill(ctx context.Context, parent string, version skills.Version, archive []byte) (string, error) {
	if skills.Digest(archive) != version.Digest {
		return "", domain.Invalid("downloaded skill content does not match its advertised digest")
	}
	if err := m.directory(parent, true); err != nil {
		return "", err
	}
	path, err := os.MkdirTemp(parent, ".staging-")
	if err != nil {
		return "", err
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.RemoveAll(path)
		}
	}()
	destination := path
	if version.Name != "" {
		destination = filepath.Join(path, version.Name)
	}
	if err := skills.Unpack(ctx, archive, destination); err != nil {
		return "", err
	}
	if version.Name == "" {
		if err := writeBuiltinDigest(path, version.Digest); err != nil {
			return "", err
		}
	}
	if err := flushManagedSkillTree(ctx, path); err != nil {
		return "", err
	}
	keep = true
	return path, nil
}

// A selected Shared path stays available to tasks or editors that already use it.
func (m *managedSkills) prepareShared(ctx context.Context, version skills.Version, archive []byte) (string, string, error) {
	parent := filepath.Join(m.sharedRoot(), skillGenerations)
	path, err := m.stageSkill(ctx, parent, version, archive)
	if err != nil {
		return "", "", err
	}
	keep := false
	defer func() {
		if !keep {
			_ = os.RemoveAll(path)
		}
	}()
	id := "generation-" + strings.TrimPrefix(filepath.Base(path), ".staging-")
	target := filepath.Join(parent, id)
	if err := moveManagedSkillPath(path, target, false); err != nil {
		return "", "", err
	}
	path = target
	if err := m.syncSkillDirs(parent); err != nil {
		return "", "", err
	}
	keep = true
	return id, path, nil
}

func (m *managedSkills) syncSkillDirs(parent string) error {
	base := filepath.Dir(m.root)
	for dir := parent; ; dir = filepath.Dir(dir) {
		if err := syncManagedSkillDir(dir); err != nil {
			return err
		}
		if dir == base {
			return nil
		}
	}
}

func (m *managedSkills) applyShared(ctx context.Context, version skills.Version, fetch func() ([]byte, error)) error {
	name := version.Name
	if !skills.ValidName(name) || !validInstalledSkill(version, name) {
		return domain.Invalid("invalid shared skill version")
	}
	state, err := m.snapshot()
	if err != nil {
		return err
	}
	entry := state.Shared[name]
	id := entry.Active
	if id == "" {
		path := m.draftPath(name, localSkillDraft)
		if _, err := os.Lstat(path); err == nil {
			id = localSkillDraft
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	base := entry.Drafts[id]
	if base.Revision >= version.Revision {
		if base.Revision == version.Revision && base != version {
			return domain.Conflict("shared skill revision has a different digest than the installed base")
		}
		return nil
	}
	if id != "" {
		digest, err := m.contentDigest(ctx, m.draftPath(name, id))
		if errors.Is(err, os.ErrNotExist) {
			return nil // Local deletion is also a change to keep.
		}
		if err != nil {
			return err
		}
		if digest == version.Digest {
			// The current bytes already match the published snapshot. Advance
			// only the base, even if a writer starts a new edit right now.
			_, err := m.selectShared(ctx, version, entry.Active, id, base, id)
			return err
		}
		if digest != base.Digest {
			return nil
		}
	}
	archive, err := fetch()
	if err != nil {
		return err
	}
	nextID, path, err := m.prepareShared(ctx, version, archive)
	if err != nil {
		return err
	}
	selected := false
	defer func() {
		if !selected {
			_ = os.RemoveAll(path)
		}
	}()
	if id == "" {
		if _, err = os.Lstat(m.draftPath(name, localSkillDraft)); err == nil {
			return nil // A new direct draft appeared during the download.
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	} else {
		digest, err := m.contentDigest(ctx, m.draftPath(name, id))
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil || digest != base.Digest {
			return err
		}
	}
	selected, err = m.selectShared(ctx, version, entry.Active, id, base, nextID)
	return err
}

func (m *managedSkills) selectShared(ctx context.Context, version skills.Version, expectedActive, checkedID string, base skills.Version, nextID string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return false, err
	}
	entry := m.state.Load().Shared[version.Name]
	if entry.Active != expectedActive || entry.Drafts[checkedID] != base {
		return false, domain.Conflict("shared skill base changed during sync; retry on the next refresh")
	}
	return true, m.saveSharedLocked(version, entry, nextID, nextID)
}

func (m *managedSkills) saveSharedLocked(version skills.Version, entry installedShared, active, id string) error {
	next := *m.state.Load()
	next.Shared = maps.Clone(next.Shared)
	if next.Shared == nil {
		next.Shared = make(map[string]installedShared)
	}
	entry.Active = active
	entry.Drafts = maps.Clone(entry.Drafts)
	if entry.Drafts == nil {
		entry.Drafts = make(map[string]skills.Version)
	}
	entry.Drafts[id] = version
	next.Shared[version.Name] = entry
	return m.saveLocked(next)
}

func (m *managedSkills) acknowledge(ctx context.Context, name, id string, version skills.Version) error {
	if !skills.ValidName(name) || !validInstalledSkill(version, name) || id != localSkillDraft && !validSkillGeneration(id) {
		return domain.Invalid("invalid published skill acknowledgement")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.loadErr != nil {
		return m.loadErr
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	entry := m.state.Load().Shared[name]
	base, known := entry.Drafts[id]
	if id != localSkillDraft && !known {
		return domain.Missing("skill draft", id)
	}
	if version.Revision < base.Revision || version == base {
		return nil // A delayed receipt must never move the local base backwards.
	}
	if version.Revision == base.Revision {
		return domain.Conflict("published skill digest differs at the same revision")
	}
	if entry.Active == "" {
		entry.Active = id
	}
	return m.saveSharedLocked(version, entry, entry.Active, id)
}

func (m *managedSkills) export(ctx context.Context, name, id string) ([]byte, error) {
	if !skills.ValidName(name) {
		return nil, domain.Invalid("invalid shared skill name")
	}
	state, err := m.snapshot()
	if err != nil {
		return nil, err
	}
	entry := state.Shared[name]
	if id == "" {
		id = entry.Active
		if id == "" {
			id = localSkillDraft
		}
	}
	if _, exists := entry.Drafts[id]; id != localSkillDraft && !exists {
		return nil, domain.Missing("skill draft", id)
	}
	archive, _, err := m.pack(ctx, m.draftPath(name, id))
	if errors.Is(err, os.ErrNotExist) {
		return nil, domain.Missing("skill draft", id)
	}
	return archive, err
}

func (m *managedSkills) directDraftNames() ([]string, error) {
	if err := m.directory(m.sharedRoot(), false); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	dir, err := os.Open(m.sharedRoot())
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	entries, err := dir.ReadDir(maxDiscoveryEntries + 1)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	if len(entries) > maxDiscoveryEntries {
		return nil, domain.Invalid("too many shared skill drafts")
	}
	var names []string
	for _, entry := range entries {
		if entry.IsDir() && skills.ValidName(entry.Name()) {
			names = append(names, entry.Name())
		}
	}
	return names, nil
}

func (m *managedSkills) discoveryRoots() ([]DiscoveryRoot, error) {
	state, err := m.snapshot()
	if err != nil {
		return nil, err
	}
	var roots []DiscoveryRoot
	var issues []error
	add := func(path, scope string) {
		if err := m.directory(path, false); err == nil {
			roots = append(roots, DiscoveryRoot{Path: path, Scope: scope, Status: "ok"})
		} else if !errors.Is(err, os.ErrNotExist) {
			issues = append(issues, err)
		}
	}
	if state.Builtin.Version.Digest != "" {
		roots = append(roots, DiscoveryRoot{Path: m.builtinPath(state.Builtin), Scope: builtinSkillScope, Status: "ok"})
	}
	for name, entry := range state.Shared {
		if entry.Active != "" {
			add(m.draftPath(name, entry.Active), sharedSkillScope)
		}
	}
	names, err := m.directDraftNames()
	if err != nil {
		issues = append(issues, err)
	}
	for _, name := range names {
		if state.Shared[name].Active == "" {
			add(m.draftPath(name, localSkillDraft), sharedSkillScope)
		}
	}
	sort.Slice(roots, func(i, j int) bool { return roots[i].Path < roots[j].Path })
	return roots, errors.Join(issues...)
}

func (m *managedSkills) status(ctx context.Context) (LocalSkillsStatus, error) {
	state, loadErr := m.snapshot()
	status := LocalSkillsStatus{BuiltinRoot: m.builtinRoot(), SharedRoot: m.sharedRoot(), Shared: []LocalSkill{}}
	var syncErr error
	if current := m.syncErr.Load(); current != nil {
		syncErr = *current
	}
	if err := errors.Join(loadErr, syncErr); err != nil {
		status.SyncError = err.Error()
	}
	if loadErr != nil {
		return status, nil
	}
	if bundle := state.Builtin; bundle.Version.Digest != "" {
		status.Builtin = &LocalSkill{Path: m.builtinPath(bundle), Digest: bundle.Version.Digest, DraftID: bundle.ID, Active: true}
	}
	inspect := func(name, id string, base skills.Version, active bool) {
		path := m.draftPath(name, id)
		digest, err := m.contentDigest(ctx, path)
		value := LocalSkill{Name: name, Revision: base.Revision, Digest: base.Digest, ContentDigest: digest, Path: path,
			DraftID: id, Active: active, Dirty: err != nil || digest != base.Digest}
		if err != nil {
			value.Error = err.Error()
		}
		if active || value.Dirty && !errors.Is(err, os.ErrNotExist) {
			status.Shared = append(status.Shared, value)
		}
	}
	for name, entry := range state.Shared {
		for id, version := range entry.Drafts {
			if err := ctx.Err(); err != nil {
				return LocalSkillsStatus{}, err
			}
			inspect(name, id, version, id == entry.Active)
		}
	}
	names, err := m.directDraftNames()
	if err != nil {
		return LocalSkillsStatus{}, fmt.Errorf("list local skill drafts: %w", err)
	}
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return LocalSkillsStatus{}, err
		}
		entry := state.Shared[name]
		if _, known := entry.Drafts[localSkillDraft]; !known {
			inspect(name, localSkillDraft, skills.Version{}, entry.Active == "")
		}
	}
	sort.Slice(status.Shared, func(i, j int) bool {
		a, b := status.Shared[i], status.Shared[j]
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		if a.Active != b.Active {
			return a.Active
		}
		return a.DraftID < b.DraftID
	})
	return status, nil
}
