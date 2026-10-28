package persistence

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/oines/astral/internal/domain"
)

// PurgeSessionDeliveries removes routes after Core and Recall have been erased.
func (tx *Tx) PurgeSessionDeliveries(ctx context.Context, id string) error {
	_, err := tx.tx.ExecContext(ctx, "DELETE FROM deliveries WHERE session_id=?", id)
	return err
}

// PurgeAgentData erases owned current documents and all revisions, including
// accounts previously deleted from the current configuration. Ownership is
// collected before deleting records so retained receipts cannot become orphans.
func (tx *Tx) PurgeAgentData(ctx context.Context, agentID string) error {
	type document struct {
		kind, id string
		fields   map[string]json.RawMessage
	}
	rows, err := tx.tx.QueryContext(ctx, "SELECT kind,id,data FROM documents UNION ALL SELECT kind,id,data FROM versions")
	if err != nil {
		return err
	}
	var documents []document
	for rows.Next() {
		var d document
		var raw []byte
		if err = rows.Scan(&d.kind, &d.id, &raw); err != nil {
			break
		}
		if err = json.Unmarshal(raw, &d.fields); err != nil {
			break
		}
		documents = append(documents, d)
	}
	if err == nil {
		err = rows.Err()
	}
	err = closeRows(rows, err)
	if err != nil {
		return err
	}
	value := func(d document, field string) string {
		var result string
		_ = json.Unmarshal(d.fields[field], &result)
		return result
	}
	key := func(kind, id string) string { return kind + "\x00" + id }
	owned := map[string]bool{}
	connections, conversations, bindings := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for changed := true; changed; {
		changed = false
		for _, d := range documents {
			if d.kind == "agents" || d.kind == "sessions" || d.kind == "onboarding" {
				continue
			}
			matches := value(d, "agent_id") == agentID || connections[value(d, "connection_id")] || conversations[value(d, "conversation_id")] || bindings[value(d, "binding_id")]
			matches = matches || d.kind == "channel_cursors" && connections[d.id] || (d.kind == "inbox_cursors" || d.kind == "probe_cursors" || d.kind == "im_activation_cursors") && bindings[d.id]
			if !matches || owned[key(d.kind, d.id)] {
				continue
			}
			owned[key(d.kind, d.id)], changed = true, true
			switch d.kind {
			case "connections":
				connections[d.id] = true
			case "conversations":
				conversations[d.id] = true
			case "bindings":
				bindings[d.id] = true
			}
		}
	}
	credentials := map[string]bool{}
	retainedCredentials := map[string]bool{}
	for _, d := range documents {
		for field := range d.fields {
			if field != "credential_id" && !strings.HasSuffix(field, "_credential_id") {
				continue
			}
			id := value(d, field)
			if id == "" {
				continue
			}
			if owned[key(d.kind, d.id)] {
				credentials[id] = true
			} else {
				retainedCredentials[id] = true
			}
		}
	}
	for id := range connections {
		if _, err = tx.tx.ExecContext(ctx, "DELETE FROM deliveries WHERE event_id IN (SELECT id FROM observations WHERE connection_id=?)", id); err != nil {
			return err
		}
		if _, err = tx.tx.ExecContext(ctx, "DELETE FROM observations WHERE connection_id=?", id); err != nil {
			return err
		}
	}
	for _, d := range documents {
		if !owned[key(d.kind, d.id)] {
			continue
		}
		if d.kind == "outbox" {
			if _, err = tx.tx.ExecContext(ctx, "DELETE FROM outbox_keys WHERE outbox_id=?", d.id); err != nil {
				return err
			}
		}
		for _, table := range []string{"documents", "versions"} {
			if _, err = tx.tx.ExecContext(ctx, "DELETE FROM "+table+" WHERE kind=? AND id=?", d.kind, d.id); err != nil {
				return err
			}
		}
		delete(owned, key(d.kind, d.id))
	}
	for id := range credentials {
		if !retainedCredentials[id] {
			if err = tx.DeleteSecret(ctx, id); err != nil {
				return err
			}
		}
	}
	_, err = tx.tx.ExecContext(ctx, "DELETE FROM operations WHERE json_extract(data,'$.agent_id')=?", agentID)
	return err
}

func closeRows(rows interface{ Close() error }, err error) error {
	closeErr := rows.Close()
	if err != nil {
		return err
	}
	return closeErr
}

// RequireActiveAgent is checked inside resource-creation transactions so a
// caller that resolved configuration before deletion cannot resurrect data.
func (tx *Tx) RequireActiveAgent(ctx context.Context, id string) error {
	var agent domain.Agent
	if err := tx.Get(ctx, "agents", id, &agent); err != nil {
		return err
	}
	if agent.Deletion {
		return domain.Missing("Bot", id)
	}
	return nil
}

// PurgeUnownedWeixinWire is used only by an explicitly requested maintenance
// operation. Attributed cache entries are never eligible for this cleanup.
func (tx *Tx) PurgeUnownedWeixinWire(ctx context.Context, id string) error {
	var current struct {
		domain.Meta
		ConnectionID string `json:"connection_id"`
	}
	if err := tx.Get(ctx, "weixin_wire", id, &current); err != nil {
		return err
	}
	rows, err := tx.tx.QueryContext(ctx, "SELECT data FROM documents WHERE kind='weixin_wire' AND id=? UNION SELECT data FROM versions WHERE kind='weixin_wire' AND id=?", id, id)
	if err != nil {
		return err
	}
	credentials := map[string]bool{}
	for rows.Next() {
		var raw []byte
		var record struct {
			ConnectionID string `json:"connection_id"`
			CredentialID string `json:"credential_id"`
		}
		if err = rows.Scan(&raw); err != nil {
			break
		}
		if err = json.Unmarshal(raw, &record); err != nil {
			break
		}
		if record.ConnectionID != "" {
			err = domain.Conflict("an attributed Weixin cache cannot be removed as anonymous data")
			break
		}
		if record.CredentialID != "" {
			credentials[record.CredentialID] = true
		}
	}
	if err == nil {
		err = rows.Err()
	}
	if err = closeRows(rows, err); err != nil {
		return err
	}
	if err = tx.Purge(ctx, "weixin_wire", id, current.Revision); err != nil {
		return err
	}
	for credential := range credentials {
		var references int
		if err = tx.tx.QueryRowContext(ctx, "SELECT count(*) FROM (SELECT data FROM documents UNION ALL SELECT data FROM versions) WHERE json_extract(data,'$.credential_id')=? OR json_extract(data,'$.headers_credential_id')=?", credential, credential).Scan(&references); err != nil {
			return err
		}
		if references == 0 {
			if err = tx.DeleteSecret(ctx, credential); err != nil {
				return err
			}
		}
	}
	return nil
}
