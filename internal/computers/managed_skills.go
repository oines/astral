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

type installedShared struct {
	Version skills.Version `json:"version"`
	Pending *sharedInstall `json:"pending,omitempty"`
}

type installedSkills struct {
	Builtin skills.Version             `json:"builtin"`
	Shared  map[string]installedShared `json:"shared"`
}

type managedSkills struct {
	root          string
	canonicalRoot string
	mu            sync.Mutex   // Serializes durable state changes, never held by discovery.
	builtinMu     sync.RWMutex // Keeps Built-in discovery clear of directory renames.
	sharedMu      sync.RWMutex // Keeps Shared readers clear of directory replacement.
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
		for name, entry := range state.Shared {
			if entry.Pending != nil {
				err = errors.Join(err, m.recoverSharedLocked(name, entry))
			}
		}
		err = errors.Join(err, m.cleanSharedStages())
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

func (m *managedSkills) sharedPath(name string) string { return filepath.Join(m.sharedRoot(), name) }

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
		state.Builtin != (skills.Version{}) && !validInstalledSkill(state.Builtin, "") {
		return errors.New("invalid managed skills state")
	}
	for name, entry := range state.Shared {
		if !skills.ValidName(name) || entry.Version != (skills.Version{}) && !validInstalledSkill(entry.Version, name) {
			return errors.New("invalid shared skill state")
		}
		if entry.Pending != nil && (!validSharedStage(entry.Pending.Stage) || !validInstalledSkill(entry.Pending.Version, name) || entry.Pending.Version.Revision <= entry.Version.Revision) {
			return errors.New("invalid pending shared skill update")
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
	m.state.Store(&state)
	return syncManagedSkillDir(m.root)
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

func (m *managedSkills) saveSharedEntryLocked(name string, entry installedShared) error {
	next := *m.state.Load()
	next.Shared = maps.Clone(next.Shared)
	if entry == (installedShared{}) {
		delete(next.Shared, name)
	} else {
		if next.Shared == nil {
			next.Shared = make(map[string]installedShared)
		}
		next.Shared[name] = entry
	}
	return m.saveLocked(next)
}

func (m *managedSkills) acknowledge(ctx context.Context, version skills.Version) error {
	name := version.Name
	if !skills.ValidName(name) || !validInstalledSkill(version, name) {
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
	if entry.Pending != nil {
		if err := m.recoverSharedLocked(name, entry); err != nil {
			return err
		}
		entry = m.state.Load().Shared[name]
	}
	base := entry.Version
	if version.Revision < base.Revision || version == base {
		return nil // A delayed receipt must never move the local base backwards.
	}
	if version.Revision == base.Revision {
		return domain.Conflict("published skill digest differs at the same revision")
	}
	return m.saveSharedEntryLocked(name, installedShared{Version: version})
}

func (m *managedSkills) export(ctx context.Context, name string) ([]byte, error) {
	if !skills.ValidName(name) {
		return nil, domain.Invalid("invalid shared skill name")
	}
	m.sharedMu.RLock()
	defer m.sharedMu.RUnlock()
	if m.loadErr != nil {
		return nil, m.loadErr
	}
	archive, _, err := m.pack(ctx, m.sharedPath(name))
	if errors.Is(err, os.ErrNotExist) {
		return nil, domain.Missing("local Shared Skill", name)
	}
	return archive, err
}

func (m *managedSkills) localSharedNames() ([]string, error) {
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
		return nil, domain.Invalid("too many local Shared Skills")
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
	if state.Builtin.Digest != "" {
		roots = append(roots, DiscoveryRoot{Path: m.builtinRoot(), Scope: builtinSkillScope, Status: "ok"})
	}
	m.sharedMu.RLock()
	names, err := m.localSharedNames()
	m.sharedMu.RUnlock()
	for _, name := range names {
		roots = append(roots, DiscoveryRoot{Path: m.sharedPath(name), Scope: sharedSkillScope, Status: "ok"})
	}
	sort.Slice(roots, func(i, j int) bool { return roots[i].Path < roots[j].Path })
	return roots, err
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
	if state.Builtin.Digest != "" {
		status.Builtin = &LocalSkill{Path: m.builtinRoot(), Digest: state.Builtin.Digest}
	}
	inspect := func(name string) {
		path := m.sharedPath(name)
		// Read the base and pin the directory before a sync can swap it.
		m.mu.Lock()
		base := m.state.Load().Shared[name].Version
		m.sharedMu.RLock()
		m.mu.Unlock()
		digest, err := m.contentDigest(ctx, path)
		m.sharedMu.RUnlock()
		value := LocalSkill{
			Name: name, Path: path, Revision: base.Revision, Digest: base.Digest,
			ContentDigest: digest, Dirty: err != nil || digest != base.Digest,
		}
		if err != nil {
			value.Error = err.Error()
		}
		status.Shared = append(status.Shared, value)
	}
	for name := range state.Shared {
		if err := ctx.Err(); err != nil {
			return LocalSkillsStatus{}, err
		}
		inspect(name)
	}
	m.sharedMu.RLock()
	names, err := m.localSharedNames()
	m.sharedMu.RUnlock()
	if err != nil {
		return LocalSkillsStatus{}, fmt.Errorf("list local Shared Skills: %w", err)
	}
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return LocalSkillsStatus{}, err
		}
		if _, known := state.Shared[name]; !known {
			inspect(name)
		}
	}
	sort.Slice(status.Shared, func(i, j int) bool { return status.Shared[i].Name < status.Shared[j].Name })
	return status, nil
}
