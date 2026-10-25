package channels

import (
	"context"
	"encoding/json"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

type WeixinWireRepairResult struct {
	Attributed int `json:"attributed"`
	Unowned    int `json:"unowned"`
	Purged     int `json:"purged"`
}

// RepairWeixinWireOwnership is an explicit one-time maintenance operation.
// Normal runtime and Bot deletion never guess ownership or invoke this repair.
// purgeUnowned authorizes erasing only the remaining anonymous protocol caches.
func RepairWeixinWireOwnership(ctx context.Context, store *persistence.Store, purgeUnowned bool) (WeixinWireRepairResult, error) {
	var result WeixinWireRepairResult
	items, err := store.Retained(ctx, "connections")
	if err != nil {
		return result, err
	}
	var adapters []*weixinAdapter
	seen := map[string]bool{}
	for _, item := range items {
		var connection domain.Connection
		if err = json.Unmarshal(item.Data, &connection); err != nil {
			return result, err
		}
		if connection.Adapter != "weixin.ilink" {
			continue
		}
		identities := []string{connection.AccountID}
		secret, secretErr := store.Secret(ctx, connection.CredentialID)
		if secretErr == nil {
			var credential WeixinCredential
			if json.Unmarshal([]byte(secret), &credential) == nil {
				identities = append(identities, credential.BotID)
			}
		} else if ctx.Err() != nil {
			return result, ctx.Err()
		}
		for _, botID := range identities {
			key := connection.ID + "\x00" + botID
			if botID == "" || seen[key] {
				continue
			}
			seen[key] = true
			adapters = append(adapters, &weixinAdapter{connection: connection, credential: WeixinCredential{BotID: botID}})
		}
	}
	conversations, err := store.Retained(ctx, "conversations")
	if err != nil {
		return result, err
	}
	keys := map[string]string{}
	for _, item := range conversations {
		var conversation domain.Conversation
		if err = json.Unmarshal(item.Data, &conversation); err != nil {
			return result, err
		}
		for _, adapter := range adapters {
			if adapter.connection.ID != conversation.ConnectionID {
				continue
			}
			keys[adapter.wireID("context", conversation.ExternalID)] = conversation.ConnectionID
			var after int64
			for {
				page, err := store.Observations(ctx, conversation.ID, after, 500)
				if err != nil {
					return result, err
				}
				for _, event := range page {
					for _, attachment := range event.Observation.Attachments {
						keys[adapter.wireID("media", attachment.ExternalID)] = conversation.ConnectionID
					}
					after = event.Sequence
				}
				if len(page) < 500 {
					break
				}
			}
		}
	}
	err = store.Update(ctx, func(tx *persistence.Tx) error {
		items, err := tx.List(ctx, "weixin_wire")
		if err != nil {
			return err
		}
		for _, item := range items {
			var record weixinWireRecord
			if err := json.Unmarshal(item.Data, &record); err != nil {
				return err
			}
			if record.ConnectionID != "" {
				continue
			}
			if connectionID := keys[record.ID]; connectionID != "" {
				record.ConnectionID = connectionID
				if err := tx.Save(ctx, "weixin_wire", &record.Meta, &record); err != nil {
					return err
				}
				result.Attributed++
			} else if purgeUnowned {
				if err := tx.PurgeUnownedWeixinWire(ctx, record.ID); err != nil {
					return err
				}
				result.Purged++
			} else {
				result.Unowned++
			}
		}
		return nil
	})
	if err != nil {
		return WeixinWireRepairResult{}, err
	}
	return result, nil
}
