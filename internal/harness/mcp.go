package harness

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/mcpclient"
)

const mcpMountGrace = 5 * time.Minute

var errMCPMountOffline = domain.Offline("the remote MCP tool is offline")

type mcpBinding struct {
	definition core.ToolDefinition
	server     string
	remote     string
}

type mcpCatalog struct {
	fingerprint string
	ordered     []mcpBinding
	byName      map[string]mcpBinding
}

// A mount is owned by one ACP connection and shared by the root's delegated
// Sessions. During disconnect grace only the credential-free catalog survives.
type mcpMount struct {
	owner   string
	lease   context.Context
	catalog *mcpCatalog
	clients map[string]*mcpclient.Client
	permit  chan struct{}
	timer   *time.Timer
	ready   bool      // guarded by Service.mcpMu
	prior   *mcpMount // current ready generation during a deferred replacement
}

func (m *mcpMount) serving() *mcpMount {
	if m == nil || m.ready {
		return m
	}
	return m.prior
}

func closeMCPClients(clients map[string]*mcpclient.Client) {
	for _, client := range clients {
		_ = client.Close()
	}
}

func mcpName(value string) string {
	name := strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + ('a' - 'A')
		}
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_' {
			return r
		}
		return '_'
	}, strings.TrimSpace(value))
	return strings.Trim(name, "_")
}

func openMCPCatalog(ctx context.Context, specs []mcpclient.ServerSpec) (*mcpCatalog, map[string]*mcpclient.Client, error) {
	if len(specs) > 8 {
		return nil, nil, domain.Invalid("a Session may connect to at most eight MCP servers")
	}
	catalog := &mcpCatalog{byName: make(map[string]mcpBinding)}
	clients := make(map[string]*mcpclient.Client)
	success := false
	defer func() {
		if !success {
			closeMCPClients(clients)
		}
	}()
	for _, spec := range specs {
		if spec.Type != "http" && spec.Type != "sse" {
			return nil, nil, domain.Invalid("MCP requires a remote HTTP or SSE server")
		}
		name := mcpName(spec.Name)
		if name == "" || len(name) > 40 || clients[name] != nil {
			return nil, nil, domain.Invalid("MCP server names must be unique and at most 40 characters after normalization")
		}
		client, err := mcpclient.Connect(ctx, spec, mcpclient.Options{})
		if err != nil {
			return nil, nil, domain.Offline("could not connect to the remote MCP server")
		}
		clients[name] = client
		for _, tool := range client.Tools() {
			normalized := mcpName(tool.Name)
			fullName := "mcp__" + name + "__" + normalized
			if len(catalog.ordered) == mcpclient.MaxTools {
				return nil, nil, domain.Invalid("too many MCP tools for this Session")
			}
			if normalized == "" || len(fullName) > 64 || catalog.byName[fullName].remote != "" {
				return nil, nil, domain.Invalid("MCP tool names must be unique after normalization and at most 64 characters")
			}
			var schema map[string]any
			decoder := json.NewDecoder(bytes.NewReader(tool.InputSchema))
			decoder.UseNumber()
			if decoder.Decode(&schema) != nil || schema == nil {
				return nil, nil, domain.Invalid("invalid MCP tool input schema")
			}
			binding := mcpBinding{
				definition: core.ToolDefinition{Name: fullName, Description: tool.Description, Parameters: encode(schema), Version: "astral-mcp-1"},
				server:     name, remote: tool.Name,
			}
			catalog.ordered = append(catalog.ordered, binding)
			catalog.byName[fullName] = binding
		}
	}
	slices.SortFunc(catalog.ordered, func(a, b mcpBinding) int {
		if n := strings.Compare(a.server, b.server); n != 0 {
			return n
		}
		return strings.Compare(a.remote, b.remote)
	})
	digest := sha256.New()
	for _, item := range catalog.ordered {
		// Delimit the underlying names too so an older wrapper cannot invoke
		// a different remote operation that happens to normalize identically.
		digest.Write(encode(item.definition))
		digest.Write(encode(item.remote))
	}
	catalog.fingerprint = hex.EncodeToString(digest.Sum(nil))
	success = true
	return catalog, clients, nil
}

func (s *Service) MountACPTools(ctx context.Context, sessionID, connectionID string, specs []mcpclient.ServerSpec) error {
	setup, err := s.PrepareACPMount(ctx, sessionID, connectionID, specs)
	if err != nil {
		return err
	}
	defer setup.Discard()
	return setup.Apply(sessionID)
}

// ACPMountSetup owns the resources discovered for one ACP setup request until
// session setup succeeds. Discard closes only this request's unused clients.
// The setup request calls Apply at most once and always defers Discard.
type ACPMountSetup struct {
	service      *Service
	ctx          context.Context
	connectionID string
	root         *runtime
	next         *mcpMount
}

func (s *Service) PrepareACPMount(ctx context.Context, sessionID, connectionID string, specs []mcpclient.ServerSpec) (*ACPMountSetup, error) {
	if connectionID == "" {
		return nil, domain.Invalid("missing ACP connection identity")
	}
	setup := &ACPMountSetup{service: s, ctx: ctx, connectionID: connectionID}
	if sessionID != "" {
		root, err := s.openRunnable(ctx, sessionID)
		if err != nil {
			return nil, err
		}
		if root.subagent != nil {
			return nil, domain.Forbidden("MCP mounts belong to a root Session")
		}
		setup.root = root
		s.mcpMu.Lock()
		current := s.mcpMounts[sessionID]
		s.mcpMu.Unlock()
		if len(specs) != 0 && current != nil && current.owner != "" && current.owner != connectionID {
			return nil, domain.Conflict("another ACP connection owns this Session's MCP mount")
		}
	}
	next, err := s.prepareMCPMount(ctx, connectionID, specs)
	if err != nil {
		return nil, err
	}
	setup.next = next
	return setup, nil
}

func (m *ACPMountSetup) Apply(sessionID string) error {
	root := m.root
	if root == nil {
		var err error
		root, err = m.service.openRunnable(m.ctx, sessionID)
		if err != nil {
			return err
		}
	}
	if root.id != sessionID || root.subagent != nil {
		return domain.Forbidden("MCP mounts belong to a root Session")
	}
	next := m.next
	m.next = nil
	return root.installMCPMount(m.ctx, m.connectionID, next)
}

func (m *ACPMountSetup) Discard() {
	if m.next != nil {
		closeMCPClients(m.next.clients)
		m.next = nil
	}
}

func (s *Service) prepareMCPMount(ctx context.Context, owner string, specs []mcpclient.ServerSpec) (*mcpMount, error) {
	if len(specs) == 0 {
		return nil, nil
	}
	catalog, clients, err := openMCPCatalog(ctx, specs)
	if err != nil {
		return nil, err
	}
	if len(catalog.ordered) == 0 {
		closeMCPClients(clients)
		return nil, domain.Invalid("the MCP servers exposed no tools")
	}
	return &mcpMount{owner: owner, lease: ctx, catalog: catalog, clients: clients, permit: make(chan struct{}, 8)}, nil
}

func (r *runtime) installMCPMount(ctx context.Context, connectionID string, next *mcpMount) error {
	s, sessionID := r.service, r.id
	committed := false
	defer func() {
		if next != nil && !committed {
			closeMCPClients(next.clients)
		}
	}()
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	var record domain.Session
	if err := s.Store.Get(ctx, "sessions", sessionID, &record); err != nil {
		return err
	}
	if record.Private || record.Archived || record.Deletion != nil {
		return domain.Forbidden("the Session is unavailable to ACP")
	}
	s.mcpMu.Lock()
	if err := ctx.Err(); err != nil {
		s.mcpMu.Unlock()
		return err
	}
	current := s.mcpMounts[sessionID]
	if current != nil && current.owner != "" && current.owner != connectionID {
		s.mcpMu.Unlock()
		if next == nil {
			return nil
		}
		return domain.Conflict("another ACP connection owns this Session's MCP mount")
	}
	if next == nil && (current == nil || current.owner != connectionID) {
		s.mcpMu.Unlock()
		return nil
	}
	if current != nil && next != nil && current.catalog.fingerprint == next.catalog.fingerprint {
		next.catalog, next.permit = current.catalog, current.permit
	}
	var prior *mcpMount
	if next != nil {
		prior = current.serving()
		if prior != nil && prior.owner == connectionID {
			next.prior = prior
		} else {
			prior = nil
		}
	}
	s.setMCPMountLocked(sessionID, next)
	s.mcpMu.Unlock()
	if err := r.syncMCPConfig(ctx, record); err != nil {
		s.mcpMu.Lock()
		if s.mcpMounts[sessionID] == next {
			s.setMCPMountLocked(sessionID, current)
		}
		s.mcpMu.Unlock()
		return err
	}
	committed = true
	if current != nil {
		if current.timer != nil {
			current.timer.Stop()
		}
		if current != prior {
			closeMCPClients(current.clients)
		}
		if current.prior != nil && current.prior != prior {
			closeMCPClients(current.prior.clients)
		}
	}
	s.notify(sessionID)
	return nil
}

func (s *Service) setMCPMountLocked(id string, mount *mcpMount) {
	if mount == nil {
		delete(s.mcpMounts, id)
	} else {
		if s.mcpMounts == nil {
			s.mcpMounts = make(map[string]*mcpMount)
		}
		s.mcpMounts[id] = mount
	}
	s.mcpActive.Store(len(s.mcpMounts) != 0)
}

func (s *Service) ReleaseACPMounts(connectionID string) {
	if connectionID == "" || !s.mcpActive.Load() {
		return
	}
	s.mcpMu.Lock()
	defer s.mcpMu.Unlock()
	for id, mount := range s.mcpMounts {
		if mount.owner != connectionID {
			continue
		}
		serving := mount.serving()
		closeMCPClients(mount.clients)
		if mount.prior != nil {
			closeMCPClients(mount.prior.clients)
		}
		if serving == nil {
			s.setMCPMountLocked(id, nil)
			continue
		}
		grace := &mcpMount{catalog: serving.catalog, permit: serving.permit, ready: true}
		s.setMCPMountLocked(id, grace)
		grace.timer = time.AfterFunc(mcpMountGrace, func() {
			s.mcpMu.Lock()
			if s.mcpMounts[id] != grace {
				s.mcpMu.Unlock()
				return
			}
			s.setMCPMountLocked(id, nil)
			s.mcpMu.Unlock()
			r, err := s.openRunnable(s.ctx, id)
			if err == nil {
				r.mcpDirty.Store(true)
				r.applyIdleMCP()
			}
		})
	}
}

func (s *Service) closeMCPMounts() {
	s.mcpMu.Lock()
	defer s.mcpMu.Unlock()
	for _, mount := range s.mcpMounts {
		if mount.timer != nil {
			mount.timer.Stop()
		}
		closeMCPClients(mount.clients)
		if mount.prior != nil {
			closeMCPClients(mount.prior.clients)
		}
	}
	s.mcpMounts = nil
	s.mcpActive.Store(false)
}

func (s *Service) mcpCatalog(root string, desired bool) *mcpCatalog {
	if !s.mcpActive.Load() {
		return nil
	}
	s.mcpMu.Lock()
	defer s.mcpMu.Unlock()
	mount := s.mcpMounts[root]
	if !desired {
		mount = mount.serving()
	}
	if mount != nil {
		return mount.catalog
	}
	return nil
}

func (r *runtime) applyIdleMCP() {
	if !r.mcpDirty.Load() {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var record domain.Session
	if r.service.Store.Get(r.service.ctx, "sessions", r.prefixSourceID(), &record) == nil && !record.Archived && record.Deletion == nil {
		_ = r.syncMCPConfig(r.service.ctx, record)
	}
}

func (r *runtime) syncMCPConfig(ctx context.Context, record domain.Session) error {
	if !r.service.mcpActive.Load() && r.mcpDefinitions == nil {
		return nil
	}
	catalog := r.service.mcpCatalog(r.prefixSourceID(), r.id == r.prefixSourceID())
	if catalog == r.mcpDefinitions || catalog != nil && r.mcpDefinitions != nil && catalog.fingerprint == r.mcpDefinitions.fingerprint {
		r.mcpDirty.Store(false)
		r.activateMCP(catalog)
		return nil
	}
	snapshot, err := r.snapshot()
	if err != nil {
		return err
	}
	if snapshot.Running || snapshot.Maintenance {
		r.mcpDirty.Store(true)
		return nil
	}
	persist, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	previous := r.mcpDefinitions
	configuration := r.core.Load().Configuration()
	r.eventsMu.Lock()
	feed := r.events
	if feed != nil {
		feed.reopening = true
	}
	r.eventsMu.Unlock()
	failFeed := func() {
		r.eventsMu.Lock()
		defer r.eventsMu.Unlock()
		if feed != nil && r.events == feed {
			feed.finish()
		}
	}
	if err := r.closeCoreForReopen(persist, true); err != nil {
		failFeed()
		return err
	}
	if feed != nil {
		select {
		case <-feed.done:
		case <-persist.Done():
			failFeed()
		}
	}
	r.mcpDefinitions = catalog
	next, err := core.OpenSession(persist, r.coreConfig(record), r.id)
	if err == nil {
		configuration.Tools = r.configuredTools(record)
		err = next.Configure(persist, configuration)
	}
	if err != nil {
		if next != nil {
			_ = next.Close(persist)
		}
		r.mcpDefinitions = previous
		recovery, rollbackErr := core.OpenSession(persist, r.coreConfig(record), r.id)
		if rollbackErr != nil {
			failFeed()
			return errors.Join(err, rollbackErr)
		}
		return errors.Join(err, r.publishCoreGeneration(persist, recovery, &snapshot.EventSequence))
	}
	r.mcpDirty.Store(false)
	r.coreGeneration.Add(1)
	if err := r.publishCoreGeneration(persist, next, &snapshot.EventSequence); err != nil {
		return err
	}
	r.activateMCP(catalog)
	return nil
}

func (r *runtime) activateMCP(catalog *mcpCatalog) {
	if catalog == nil || r.id != r.prefixSourceID() {
		return
	}
	s := r.service
	s.mcpMu.Lock()
	var prior *mcpMount
	if mount := s.mcpMounts[r.id]; mount != nil && mount.catalog == catalog {
		mount.ready = true
		prior, mount.prior = mount.prior, nil
	}
	s.mcpMu.Unlock()
	if prior != nil {
		closeMCPClients(prior.clients)
	}
}

func (r *runtime) mountedTools() []core.Tool {
	catalog := r.mcpDefinitions
	if catalog == nil {
		return nil
	}
	tools := make([]core.Tool, 0, len(catalog.ordered))
	for _, binding := range catalog.ordered {
		tools = append(tools, core.ToolFunc{Spec: binding.definition, Run: func(ctx context.Context, inv core.Invocation, _ func(core.ToolProgress)) (core.ToolExecution, error) {
			if inv.SessionID != r.id {
				return core.ToolExecution{}, notExecuted("session_mismatch", domain.Forbidden("tool belongs to another Session"))
			}
			if err := r.requireRunnable(ctx); err != nil {
				return core.ToolExecution{}, notExecuted("session_unavailable", err)
			}
			result, err := r.service.callMCP(ctx, r.prefixSourceID(), catalog.fingerprint, binding.definition.Name, inv.Call.Arguments)
			if err != nil {
				if errors.Is(err, errMCPMountOffline) || domain.IsMissing(err) {
					return core.ToolExecution{}, notExecuted("mcp_unavailable", err)
				}
				// A failed remote response must not trigger an automatic repeat.
				// The remote server may have acted before this call lost its reply.
				return core.ToolExecution{}, &core.ToolError{
					Code: "mcp_call_failed", Outcome: core.OutcomeFailed,
					Cause: errors.New("the MCP call did not return a result; remote effects are unconfirmed"),
				}
			}
			return projectMCPResult(result), nil
		}})
	}
	return tools
}

func projectMCPResult(value mcpclient.Result) core.ToolExecution {
	result := &core.ToolResult{Outcome: core.OutcomeSucceeded, IsError: value.IsError}
	for _, part := range value.Content {
		switch part.Type {
		case "text":
			result.Content = append(result.Content, core.Content{Type: "text", Text: part.Text})
		case "image":
			if strings.HasPrefix(part.MIMEType, "image/") {
				result.Content = append(result.Content, core.Content{Type: "image", MediaType: part.MIMEType, URL: "data:" + part.MIMEType + ";base64," + part.Data})
			}
		case "resource_link":
			result.Content = append(result.Content, core.Content{Type: "text", Text: part.URI})
		case "resource":
			result.Content = append(result.Content, core.Content{Type: "text", Text: string(part.Resource)})
		}
	}
	if len(value.StructuredContent) != 0 && len(result.Content) == 0 {
		result.Content = core.Text(string(value.StructuredContent))
	}
	if value.IsError {
		result.Code = "mcp_tool_error"
	}
	return core.ToolExecution{Result: result}
}

func (s *Service) MCPTools(ctx context.Context, sessionID string) ([]core.ToolDefinition, error) {
	r, err := s.openRunnable(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	tools := make([]core.ToolDefinition, 0)
	s.mcpMu.Lock()
	defer s.mcpMu.Unlock()
	if mount := s.mcpMounts[r.prefixSourceID()].serving(); mount != nil {
		for _, binding := range mount.catalog.ordered {
			tools = append(tools, binding.definition)
		}
	}
	return tools, nil
}

func (s *Service) CallMCPTool(ctx context.Context, sessionID, name string, args json.RawMessage) (mcpclient.Result, error) {
	r, err := s.openRunnable(ctx, sessionID)
	if err != nil {
		return mcpclient.Result{}, err
	}
	return s.callMCP(ctx, r.prefixSourceID(), "", name, args)
}

func (s *Service) callMCP(ctx context.Context, root, fingerprint, name string, args json.RawMessage) (mcpclient.Result, error) {
	s.mcpMu.Lock()
	mount := s.mcpMounts[root].serving()
	if mount == nil || mount.owner == "" || mount.lease.Err() != nil || fingerprint != "" && mount.catalog.fingerprint != fingerprint {
		s.mcpMu.Unlock()
		return mcpclient.Result{}, errMCPMountOffline
	}
	binding, ok := mount.catalog.byName[name]
	client := mount.clients[binding.server]
	s.mcpMu.Unlock()
	if !ok || client == nil {
		return mcpclient.Result{}, domain.Missing("MCP tool", name)
	}
	select {
	case mount.permit <- struct{}{}:
		defer func() { <-mount.permit }()
	default:
		return mcpclient.Result{}, domain.Conflict("too many simultaneous MCP calls for this Session")
	}
	call, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(mount.lease, cancel)
	defer stop()
	return client.Call(call, binding.remote, args)
}
