package acp

import (
	"context"
	"errors"
	"strings"
	"sync"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/harness"
	"github.com/oines/astral/internal/mcpclient"
	"github.com/oines/astral/internal/persistence"
)

const sessionBuffer = 256

type loadedSession struct {
	connection *connection
	id         string
	ctx        context.Context
	cancel     context.CancelFunc
	done       chan struct{}

	mu      sync.Mutex
	ready   bool
	closing bool
	pending *pendingPrompt
}

type pendingPrompt struct {
	id        string
	runID     string
	cancelled bool
	result    chan core.Event
	done      chan struct{}
	early     []core.Event
}

func (c *connection) allowedSession(ctx context.Context, id string) (domain.Session, error) {
	var record domain.Session
	if err := c.service.harness.Store.Get(ctx, "sessions", id, &record); err != nil {
		return record, err
	}
	if record.AgentID != c.botID || record.SubagentParentID != "" || record.Private || record.Archived || record.Deletion != nil {
		return record, domain.Missing("Session", id)
	}
	return record, nil
}

func (c *connection) setup(request rpcRequest) *rpcError {
	var params struct {
		SessionID string                 `json:"sessionId"`
		CWD       string                 `json:"cwd"`
		Servers   []mcpclient.ServerSpec `json:"mcpServers"`
	}
	if err := decodeParams(request.Params, &params); err != nil {
		return err
	}
	if !absoluteCWD(params.CWD) {
		return invalidParams("cwd must be an absolute Computer path.")
	}
	for _, server := range params.Servers {
		if server.Type != "http" && server.Type != "sse" {
			return invalidParams("MCP supports only remote HTTP and SSE servers.")
		}
	}
	h := c.service.harness
	creating := request.Method == "session/new"
	replaying := request.Method == "session/load"
	var record domain.Session
	var mount *harness.ACPMountSetup
	var err error
	if !creating {
		if params.SessionID == "" {
			return invalidParams("sessionId is required.")
		}
		record, err = c.allowedSession(c.ctx, params.SessionID)
	} else {
		if len(params.Servers) != 0 {
			if err := availableBot(c.ctx, h.Store, c.botID); err != nil {
				return toRPCError(err)
			}
		}
		if !c.reserveCreation() {
			return &rpcError{Code: rpcOverloaded, Message: "Too many sessions on this connection."}
		}
		mount, err = h.PrepareACPMount(c.ctx, "", c.id, params.Servers)
		if err == nil {
			defer mount.Discard()
			record, err = h.CreateACPSession(c.ctx, c.botID, params.CWD)
		}
		if err != nil {
			c.releaseCreation()
		}
	}
	if err != nil {
		return toRPCError(err)
	}
	loaded, previous, err := c.reserveSession(record.ID, creating)
	if err != nil {
		return toRPCError(err)
	}
	var live bool
	defer func() {
		if !live {
			c.finishSession(loaded, previous)
		}
	}()
	watch := h.Store.Watch(persistence.ChangeKey{Kind: "sessions", ID: record.ID})
	defer func() {
		if !live {
			watch.Close()
		}
	}()
	record, err = c.allowedSession(loaded.ctx, record.ID)
	if err != nil {
		return toRPCError(err)
	}
	if !creating {
		if record.CWD != "" && record.CWD != params.CWD {
			return invalidParams("cwd must match this Session's existing working directory.")
		}
		mount, err = h.PrepareACPMount(c.ctx, record.ID, c.id, params.Servers)
		if err != nil {
			return toRPCError(err)
		}
		defer mount.Discard()
	}
	sub, err := h.SubscribeSession(loaded.ctx, record.ID, sessionBuffer)
	if err != nil {
		return toRPCError(err)
	}
	defer func() {
		if !live {
			sub.Close()
		}
	}()
	view := eventView{session: loaded, title: record.Title, attempts: make(map[string]streamedParts), activeTools: make(map[string]bool)}
	if replaying {
		if err = view.replay(sub.Events, watch, record.ForkEntryID); err != nil {
			return toRPCError(err)
		}
		if err = view.replaySubagents(); err != nil {
			return toRPCError(err)
		}
	}
	selection, profiles, err := c.readModelConfig(loaded.ctx, record.ID)
	if err != nil {
		return toRPCError(err)
	}
	view.modelSelection = selection
	options, _ := selectOptions(selection, profiles)
	if options == nil {
		options = []sessionConfigOption{}
	}
	result := map[string]any{"configOptions": options}
	if creating {
		result["sessionId"] = record.ID
	}
	if err = mount.Apply(record.ID); err != nil {
		return toRPCError(err)
	}
	if previous != nil {
		previous.cancel()
		<-previous.done
	}
	loaded.mu.Lock()
	live = c.reply(request.ID, result, replaying)
	loaded.ready = live
	loaded.mu.Unlock()
	if !live {
		return nil
	}
	c.workers.Add(1)
	go func() {
		defer c.workers.Done()
		defer c.finishSession(loaded, nil)
		defer sub.Close()
		defer watch.Close()
		view.follow(sub.Events, watch)
	}()
	return nil
}

func absoluteCWD(path string) bool {
	if strings.IndexByte(path, 0) >= 0 {
		return false
	}
	return strings.HasPrefix(path, "/") || strings.HasPrefix(path, `\\`) || (len(path) >= 3 && (path[1] == ':' && (path[2] == '\\' || path[2] == '/')) && strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ", rune(path[0])))
}

func (c *connection) reserveCreation() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.sessions)+c.creating >= maxLoadedSessions {
		return false
	}
	c.creating++
	return true
}

func (c *connection) releaseCreation() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.creating--
}

func (c *connection) reserveSession(id string, created bool) (*loadedSession, *loadedSession, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if created {
		c.creating--
	}
	existing := c.sessions[id]
	if existing != nil {
		existing.mu.Lock()
		busy := !existing.ready || existing.closing || existing.pending != nil
		existing.mu.Unlock()
		if busy {
			return nil, nil, core.ErrBusy
		}
	} else if len(c.sessions)+c.creating >= maxLoadedSessions {
		return nil, nil, domain.Conflict("Too many sessions on this connection.")
	}
	ctx, cancel := context.WithCancel(c.ctx)
	loaded := &loadedSession{connection: c, id: id, ctx: ctx, cancel: cancel, done: make(chan struct{})}
	c.sessions[id] = loaded
	return loaded, existing, nil
}

func (c *connection) finishSession(loaded, previous *loadedSession) {
	loaded.cancel()
	var retired <-chan struct{}
	c.mu.Lock()
	if c.sessions[loaded.id] == loaded {
		loaded.mu.Lock()
		closing := loaded.closing
		loaded.mu.Unlock()
		if previous != nil && previous.ctx.Err() == nil && !closing {
			c.sessions[loaded.id] = previous
		} else {
			if previous != nil {
				previous.cancel()
				retired = previous.done
			}
			// Hold the slot until its mount is released so a new attachment
			// cannot install another generation with the same connection ID.
			c.service.harness.ReleaseACPMount(loaded.id, c.id)
			delete(c.sessions, loaded.id)
		}
	}
	c.mu.Unlock()
	if retired != nil {
		<-retired
	}
	close(loaded.done)
}

func (c *connection) session(id string) *loadedSession {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sessions[id]
}

func (c *connection) preparePrompt(request rpcRequest) (func() *rpcError, *rpcError) {
	var params struct {
		SessionID string         `json:"sessionId"`
		Prompt    []contentBlock `json:"prompt"`
	}
	if err := decodeParams(request.Params, &params); err != nil {
		return nil, err
	}
	input, rpcErr := promptContent(params.Prompt)
	if rpcErr != nil {
		return nil, rpcErr
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	loaded := c.sessions[params.SessionID]
	if loaded == nil {
		return nil, &rpcError{Code: -32002, Message: "Create or load this session before sending a prompt."}
	}
	pending := &pendingPrompt{id: c.id + ":" + domain.ID("input"), result: make(chan core.Event, 1), done: make(chan struct{})}
	loaded.mu.Lock()
	defer loaded.mu.Unlock()
	if !loaded.ready || loaded.closing || loaded.pending != nil || loaded.ctx.Err() != nil {
		return nil, toRPCError(core.ErrBusy)
	}
	loaded.pending = pending
	rpcID := request.ID
	return func() *rpcError {
		reason, err := loaded.prompt(pending, &input)
		loaded.mu.Lock()
		if err != nil {
			if err.Code == -32002 && err.Data == nil {
				err.Data = map[string]string{"sessionId": loaded.id}
			}
			c.fail(rpcID, err)
		} else {
			c.reply(rpcID, map[string]string{"stopReason": reason}, false)
		}
		loaded.pending = nil
		close(pending.done)
		loaded.mu.Unlock()
		return nil
	}, nil
}

func (loaded *loadedSession) prompt(pending *pendingPrompt, input *promptInput) (string, *rpcError) {
	c := loaded.connection
	if _, err := c.allowedSession(loaded.ctx, loaded.id); err != nil {
		return "", toRPCError(err)
	}
	loaded.mu.Lock()
	cancelled := pending.cancelled
	loaded.mu.Unlock()
	if cancelled {
		return "cancelled", nil
	}
	images, rpcErr := decodePromptImages(input.images)
	input.images = nil
	if rpcErr != nil {
		return "", rpcErr
	}
	h := c.service.harness
	receipt, err := h.SubmitACP(loaded.ctx, loaded.id, harness.InputRequest{
		ID: pending.id, Text: input.text, Files: input.files,
	}, c.clientName, images...)
	if receipt.RunID == "" {
		loaded.mu.Lock()
		cancelled = pending.cancelled
		loaded.mu.Unlock()
		if cancelled {
			return "cancelled", nil
		}
		if err != nil {
			return "", toRPCError(err)
		}
		return "", &rpcError{Code: -32603, Message: "The Astral run did not start."}
	}
	loaded.mu.Lock()
	pending.runID = receipt.RunID
	for _, event := range pending.early {
		if event.RunID == pending.runID {
			pending.result <- event
			break
		}
	}
	pending.early = nil
	cancelled = pending.cancelled
	loaded.mu.Unlock()
	if cancelled {
		_ = h.StopACPRun(loaded.ctx, loaded.id, receipt.RunID)
	}
	select {
	case event := <-pending.result:
		switch event.Status {
		case "cancelled":
			return "cancelled", nil
		case "failed":
			return "", &rpcError{Code: -32603, Message: "The Astral run failed. See the conversation for details."}
		}
		switch event.EndReason {
		case "max_tokens", "max_turn_requests", "refusal":
			return event.EndReason, nil
		default:
			return "end_turn", nil
		}
	case <-loaded.ctx.Done():
		return "", toRPCError(loaded.ctx.Err())
	}
}

func (c *connection) prepareClose(request rpcRequest) (func() *rpcError, *rpcError) {
	var params struct {
		SessionID string `json:"sessionId"`
	}
	if err := decodeParams(request.Params, &params); err != nil {
		return nil, err
	}
	if params.SessionID == "" {
		return nil, invalidParams("sessionId is required.")
	}
	c.mu.Lock()
	loaded := c.sessions[params.SessionID]
	if loaded == nil {
		c.mu.Unlock()
		return func() *rpcError {
			c.reply(request.ID, struct{}{}, false)
			return nil
		}, nil
	}
	loaded.mu.Lock()
	if loaded.closing {
		loaded.mu.Unlock()
		c.mu.Unlock()
		return nil, toRPCError(core.ErrBusy)
	}
	loaded.closing = true
	ready := loaded.ready
	loaded.mu.Unlock()
	c.mu.Unlock()
	return func() *rpcError {
		if ready && loaded.ctx.Err() == nil {
			if err := loaded.stopForClose(); err != nil && loaded.ctx.Err() == nil {
				loaded.mu.Lock()
				loaded.closing = false
				loaded.mu.Unlock()
				return toRPCError(err)
			}
		}
		loaded.cancel()
		select {
		case <-loaded.done:
		case <-c.ctx.Done():
			return nil
		}
		c.reply(request.ID, struct{}{}, false)
		return nil
	}, nil
}

func (loaded *loadedSession) stopForClose() error {
	c := loaded.connection
	h := c.service.harness
	snapshot, err := h.Snapshot(loaded.ctx, loaded.id)
	if err != nil {
		return err
	}
	loaded.mu.Lock()
	pending := loaded.pending
	otherRun := snapshot.Running && (pending == nil || pending.runID != snapshot.RunID)
	loaded.mu.Unlock()
	if snapshot.Maintenance || otherRun {
		return domain.Conflict("Wait for activity from outside this attachment to finish before closing the Session.")
	}
	if active, err := h.ACPHasActiveSubagents(loaded.ctx, loaded.id); err != nil {
		return err
	} else if active {
		return domain.Conflict("Wait for this Session's subagents to finish before closing the Session.")
	}
	loaded.mu.Lock()
	pending = loaded.pending
	var runID string
	if pending != nil {
		pending.cancelled = true
		runID = pending.runID
	}
	loaded.mu.Unlock()
	if pending == nil {
		return nil
	}
	if runID != "" {
		if err := h.StopACPRun(loaded.ctx, loaded.id, runID); err != nil && !errors.Is(err, core.ErrNotFound) {
			return err
		}
	}
	select {
	case <-pending.done:
		return nil
	case <-loaded.ctx.Done():
		return loaded.ctx.Err()
	}
}

func (c *connection) prepareCancellation(request rpcRequest) (func() *rpcError, *rpcError) {
	var params struct {
		SessionID string `json:"sessionId"`
	}
	if err := decodeParams(request.Params, &params); err != nil {
		return nil, err
	}
	loaded := c.session(params.SessionID)
	var runID string
	if loaded != nil {
		loaded.mu.Lock()
		if pending := loaded.pending; pending != nil {
			pending.cancelled = true
			runID = pending.runID
		}
		loaded.mu.Unlock()
	}
	return func() *rpcError {
		if runID != "" {
			if err := c.service.harness.StopACPRun(loaded.ctx, loaded.id, runID); err != nil && !errors.Is(err, core.ErrNotFound) {
				return toRPCError(err)
			}
		}
		if request.ID != nil {
			c.reply(request.ID, struct{}{}, false)
		}
		return nil
	}, nil
}
