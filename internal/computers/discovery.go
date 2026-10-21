package computers

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/oines/astral/internal/domain"
	"gopkg.in/yaml.v3"
)

const EnvironmentDiscoveryCapability = "environment.discover"

const (
	maxDiscoveryFileBytes = 1 << 20
	maxDiscoveryReadBytes = 16 << 20
	maxDiscoveryEntries   = 8192
	maxDiscoveryDepth     = 32
)

type DiscoveredSkill struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Path        string `json:"path"`
	Scope       string `json:"scope"`
	SHA256      string `json:"sha256"`
}

type DiscoveryRoot struct {
	Path   string `json:"path"`
	Scope  string `json:"scope"`
	Status string `json:"status"`
}

type DiscoveryDocument struct {
	Path    string `json:"path,omitempty"`
	Status  string `json:"status"`
	Content string `json:"content,omitempty"`
	SHA256  string `json:"sha256,omitempty"`
}

type DiscoveryIssue struct {
	Path    string `json:"path,omitempty"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// EnvironmentDiscovery contains only facts read on the bound Computer. Missing
// roots are normal; partial/unsupported/unavailable states never imply emptiness.
type EnvironmentDiscovery struct {
	ComputerID string            `json:"computer_id,omitempty"`
	CWD        string            `json:"cwd,omitempty"`
	Status     string            `json:"status"`
	SkillRoots []DiscoveryRoot   `json:"skill_roots"`
	Skills     []DiscoveredSkill `json:"skills"`
	AgentsMD   DiscoveryDocument `json:"agents_md"`
	Issues     []DiscoveryIssue  `json:"issues,omitempty"`
}

func UnavailableDiscovery(status, message string) EnvironmentDiscovery {
	return EnvironmentDiscovery{Status: status, SkillRoots: []DiscoveryRoot{}, Skills: []DiscoveredSkill{}, AgentsMD: DiscoveryDocument{Status: status}, Issues: []DiscoveryIssue{{Code: status, Message: message}}}
}

// Discover uses a boot-pinned authorized connection and an explicit capability.
// Old daemons are reported as unsupported, never scanned via the Server's disk.
func (h *Hub) Discover(ctx context.Context, b domain.ExecutionBinding) (EnvironmentDiscovery, error) {
	peer, err := h.Peer(ctx, b)
	if err != nil {
		return EnvironmentDiscovery{}, err
	}
	h.mu.RLock()
	active := h.live[b.ComputerID]
	supported := active != nil && active.peer == peer && slices.Contains(active.hello.Capabilities, EnvironmentDiscoveryCapability)
	h.mu.RUnlock()
	if !supported {
		result := UnavailableDiscovery("unsupported", "This Computer's daemon does not support environment discovery.")
		result.ComputerID, result.CWD = b.ComputerID, b.CWD
		return result, nil
	}
	var result EnvironmentDiscovery
	if err = peer.JSON(ctx, EnvironmentDiscoveryCapability, Call{Binding: b}, &result); err != nil {
		return EnvironmentDiscovery{}, err
	}
	if err = h.Validate(ctx, b); err != nil {
		return EnvironmentDiscovery{}, err
	}
	if result.ComputerID != b.ComputerID || result.CWD != b.CWD {
		return EnvironmentDiscovery{}, domain.Invalid("environment discovery returned a different execution binding")
	}
	return result, nil
}

type environmentScanner struct {
	ctx     context.Context
	result  EnvironmentDiscovery
	entries int
	bytes   int
}

func discoverEnvironment(ctx context.Context, home string, b domain.ExecutionBinding) (EnvironmentDiscovery, error) {
	s := &environmentScanner{ctx: ctx, result: EnvironmentDiscovery{ComputerID: b.ComputerID, CWD: b.CWD, Status: "ok", Skills: []DiscoveredSkill{}, SkillRoots: []DiscoveryRoot{}}}
	// Only this exact file is workspace guidance. Never walk parents.
	path := filepath.Join(b.CWD, "AGENTS.md")
	s.result.AgentsMD = DiscoveryDocument{Path: path, Status: "missing"}
	if data, err := s.read(path); err == nil {
		s.result.AgentsMD = DiscoveryDocument{Path: path, Status: "ok", Content: string(data), SHA256: fileDigest(data)}
	} else if !errors.Is(err, os.ErrNotExist) {
		s.result.AgentsMD.Status = "unreadable"
		s.issue(path, "agents_unreadable", err.Error())
	}
	for _, root := range []DiscoveryRoot{
		{Path: filepath.Join(home, ".agents", "skills"), Scope: "global", Status: "ok"},
		{Path: filepath.Join(b.CWD, ".agents", "skills"), Scope: "workspace", Status: "ok"},
	} {
		before := len(s.result.Issues)
		info, err := os.Stat(root.Path)
		switch {
		case errors.Is(err, os.ErrNotExist):
			root.Status = "missing"
		case err != nil:
			root.Status = "unreadable"
			s.issue(root.Path, "root_unreadable", err.Error())
		case !info.IsDir():
			root.Status = "invalid"
			s.issue(root.Path, "root_invalid", "Skills root must be a directory.")
		default:
			s.walk(root.Path, root.Scope, 0, map[string]bool{})
			if len(s.result.Issues) > before {
				root.Status = "partial"
			}
		}
		s.result.SkillRoots = append(s.result.SkillRoots, root)
	}
	if err := ctx.Err(); err != nil {
		return EnvironmentDiscovery{}, err
	}
	sort.Slice(s.result.Skills, func(i, j int) bool {
		a, b := s.result.Skills[i], s.result.Skills[j]
		if a.Scope != b.Scope {
			return a.Scope < b.Scope
		}
		if a.Name != b.Name {
			return a.Name < b.Name
		}
		return a.Path < b.Path
	})
	sort.Slice(s.result.Issues, func(i, j int) bool {
		a, b := s.result.Issues[i], s.result.Issues[j]
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		if a.Code != b.Code {
			return a.Code < b.Code
		}
		return a.Message < b.Message
	})
	return s.result, nil
}

func (s *environmentScanner) issue(path, code, message string) {
	s.result.Status = "partial"
	s.result.Issues = append(s.result.Issues, DiscoveryIssue{Path: path, Code: code, Message: message})
}

func (s *environmentScanner) read(path string) ([]byte, error) {
	if err := s.ctx.Err(); err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("discovery only reads regular files")
	}
	if info.Size() > maxDiscoveryFileBytes {
		return nil, errors.New("discovery file exceeds 1 MiB")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxDiscoveryFileBytes+1))
	if err != nil {
		return nil, err
	}
	s.bytes += len(data)
	if len(data) > maxDiscoveryFileBytes || s.bytes > maxDiscoveryReadBytes {
		return nil, errors.New("discovery read limit exceeded")
	}
	if !utf8.Valid(data) {
		return nil, errors.New("discovery file must be UTF-8 text")
	}
	return data, nil
}

func (s *environmentScanner) walk(path, scope string, depth int, ancestors map[string]bool) {
	if s.ctx.Err() != nil {
		return
	}
	if depth > maxDiscoveryDepth || s.entries >= maxDiscoveryEntries || s.bytes >= maxDiscoveryReadBytes {
		s.issue(path, "scan_limit", "Skills discovery limit reached; catalog is incomplete.")
		return
	}
	s.entries++
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		s.issue(path, "path_unreadable", err.Error())
		return
	}
	if ancestors[real] {
		s.issue(path, "symlink_cycle", "Skipped a cyclic skill directory link.")
		return
	}
	info, err := os.Stat(real)
	if err != nil {
		s.issue(path, "path_unreadable", err.Error())
		return
	}
	if !info.IsDir() {
		return
	}
	ancestors[real] = true
	defer delete(ancestors, real)
	dir, err := os.Open(path)
	if err != nil {
		s.issue(path, "directory_unreadable", err.Error())
		return
	}
	children, readErr := dir.ReadDir(maxDiscoveryEntries - s.entries + 1)
	_ = dir.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		s.issue(path, "directory_unreadable", readErr.Error())
		return
	}
	if len(children) > maxDiscoveryEntries-s.entries {
		s.issue(path, "scan_limit", "Skills directory contains too many entries; catalog is incomplete.")
		return
	}
	s.entries += len(children)
	sort.Slice(children, func(i, j int) bool { return children[i].Name() < children[j].Name() })
	for _, child := range children {
		if child.Name() != "SKILL.md" {
			continue
		}
		skillPath := filepath.Join(path, child.Name())
		data, err := s.read(skillPath)
		if err != nil {
			s.issue(skillPath, "skill_unreadable", err.Error())
			return
		}
		name, description, err := skillFrontmatter(data)
		if err != nil {
			s.issue(skillPath, "skill_invalid", err.Error())
			return
		}
		s.result.Skills = append(s.result.Skills, DiscoveredSkill{Name: name, Description: description, Path: skillPath, Scope: scope, SHA256: fileDigest(data)})
		// A skill directory is a package. Its examples/assets are not other skills.
		return
	}
	for _, child := range children {
		if child.IsDir() || child.Type()&os.ModeSymlink != 0 {
			if s.entries >= maxDiscoveryEntries || s.bytes >= maxDiscoveryReadBytes {
				s.issue(path, "scan_limit", "Skills discovery limit reached; catalog is incomplete.")
				break
			}
			s.walk(filepath.Join(path, child.Name()), scope, depth+1, ancestors)
		}
	}
}

func skillFrontmatter(data []byte) (string, string, error) {
	lines := strings.Split(strings.ReplaceAll(strings.TrimPrefix(string(data), "\ufeff"), "\r\n", "\n"), "\n")
	if len(lines) < 3 || strings.TrimSpace(lines[0]) != "---" {
		return "", "", errors.New("SKILL.md needs YAML frontmatter with name and description")
	}
	end := 0
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(lines[i]) == "---" || strings.TrimSpace(lines[i]) == "..." {
			end = i
			break
		}
	}
	if end == 0 {
		return "", "", errors.New("SKILL.md frontmatter is not closed")
	}
	var metadata struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	}
	if err := yaml.Unmarshal([]byte(strings.Join(lines[1:end], "\n")), &metadata); err != nil {
		return "", "", fmt.Errorf("invalid skill frontmatter: %w", err)
	}
	name, description := strings.TrimSpace(metadata.Name), strings.TrimSpace(metadata.Description)
	if name == "" || description == "" {
		return "", "", errors.New("skill name and description must be non-empty")
	}
	return name, description, nil
}

func fileDigest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
