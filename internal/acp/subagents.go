package acp

import (
	"errors"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/harness"
	"github.com/oines/astral/internal/persistence"
)

type subagentEvents struct {
	cursor      string
	initialized bool
}

type subagentUpdate struct {
	Kind      string         `json:"sessionUpdate"`
	SessionID string         `json:"sessionId"`
	Title     string         `json:"title,omitempty"`
	CWD       string         `json:"cwd,omitempty"`
	State     *subagentState `json:"state,omitempty"`
}

type subagentState struct {
	State      string `json:"state"`
	StopReason string `json:"stopReason,omitempty"`
}

func (v *eventView) watchSubagents() *persistence.Watcher {
	c := v.session.connection
	if !c.clientSubagents {
		return nil
	}
	return c.service.harness.Store.Watch(persistence.ChangeKey{Kind: persistence.SubagentActivityKind, ID: v.session.id})
}

func (v *eventView) replaySubagents() error {
	c, ctx := v.session.connection, v.session.ctx
	if !c.clientSubagents {
		return nil
	}
	last, err := c.service.harness.SubagentActivities(ctx, v.session.id, "", 1, true)
	if err != nil || len(last.Activities) == 0 {
		return err
	}
	return v.sendSubagentHistory("", last.Activities[0].Sequence, true)
}

func (v *eventView) syncSubagents() error {
	c, ctx := v.session.connection, v.session.ctx
	if !c.clientSubagents {
		return nil
	}
	if v.subagents.initialized {
		return v.sendSubagentHistory(v.subagents.cursor, 0, false)
	}
	if _, err := c.allowedSession(ctx, v.session.id); err != nil {
		return err
	}
	h := c.service.harness
	heads, err := h.Store.SubagentActivityHeads(ctx, v.session.id)
	if err != nil {
		return err
	}
	for _, activity := range heads.Activities {
		if !v.validSubagent(activity) {
			continue
		}
		view, err := h.ObserveSubagentRun(ctx, v.session.id, activity.ChildSessionID)
		var denied *domain.Error
		if errors.As(err, &denied) && (denied.Status == 403 || denied.Status == 404) {
			// The historical association can outlive a child. Resume only
			// announces children that still belong to this active session.
			continue
		}
		if err != nil {
			return err
		}
		if !v.sendSubagent(activity.ChildSessionID, view.Title, view.CWD, currentSubagentState(activity, view), true) {
			return core.ErrClosed
		}
	}
	v.subagents.initialized = true
	v.subagents.cursor = heads.Cursor
	return nil
}

func (v *eventView) sendSubagentHistory(cursor string, through uint64, replay bool) error {
	c, ctx := v.session.connection, v.session.ctx
	for {
		if _, err := c.allowedSession(ctx, v.session.id); err != nil {
			return err
		}
		page, err := c.service.harness.SubagentActivities(ctx, v.session.id, cursor, 100, false)
		if err != nil {
			return err
		}
		for _, activity := range page.Activities {
			if replay && activity.Sequence > through {
				return nil
			}
			if !v.validSubagent(activity) {
				continue
			}
			cwd := ""
			if !replay {
				var child domain.Session
				err := c.service.harness.Store.Get(ctx, "sessions", activity.ChildSessionID, &child)
				switch {
				case err == nil:
					if child.AgentID != c.botID || child.SubagentParentID != v.session.id {
						continue
					}
					cwd = child.CWD
				case !domain.IsMissing(err):
					return err
				}
			}
			if !v.sendSubagent(activity.ChildSessionID, activity.Name, cwd, recordedSubagentState(activity.Status, ""), replay) {
				return core.ErrClosed
			}
		}
		if !replay {
			v.subagents.cursor = page.Cursor
		}
		if page.NextCursor == "" {
			return nil
		}
		cursor = page.NextCursor
	}
}

func (v *eventView) validSubagent(activity domain.SubagentActivity) bool {
	return activity.SessionID == v.session.id && activity.ChildSessionID != "" && activity.ChildSessionID != v.session.id
}

func (v *eventView) sendSubagent(id, title, cwd string, state *subagentState, wait bool) bool {
	if !absoluteCWD(cwd) {
		cwd = ""
	}
	return v.session.connection.notify("session/update", struct {
		SessionID string         `json:"sessionId"`
		Update    subagentUpdate `json:"update"`
	}{v.session.id, subagentUpdate{Kind: "subagent_update", SessionID: id, Title: title, CWD: cwd, State: state}}, wait)
}

func currentSubagentState(activity domain.SubagentActivity, view harness.SubagentRunView) *subagentState {
	last := view.LastRun
	if last == nil {
		return nil
	}
	if last.ID != activity.RunID || last.Status != activity.Status || last.Status == "started" && !view.Running {
		// A journal can lag a newer run, or retain a start after a restart.
		// Wait for the durable event rather than inventing current activity.
		return &subagentState{State: "unknown"}
	}
	return recordedSubagentState(last.Status, last.EndReason)
}

func recordedSubagentState(status, reason string) *subagentState {
	switch status {
	case "started":
		return &subagentState{State: "running"}
	case "completed":
		state := &subagentState{State: "idle"}
		switch reason {
		case "end_turn", "max_tokens", "max_turn_requests", "refusal":
			state.StopReason = reason
		}
		return state
	case "failed":
		return &subagentState{State: "idle", StopReason: "error"}
	case "cancelled":
		return &subagentState{State: "idle", StopReason: "cancelled"}
	}
	return nil
}
