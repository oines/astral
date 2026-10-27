package modelbridge

import (
	"context"
	"encoding/json"
	"time"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

type RefreshResult struct {
	Added     int `json:"added"`
	Updated   int `json:"updated"`
	Removed   int `json:"removed"`
	Unchanged int `json:"unchanged"`
}

func profilesFor(records []persistence.Record, providerID string) ([]domain.ModelProfile, error) {
	profiles := []domain.ModelProfile{}
	for _, record := range records {
		var profile domain.ModelProfile
		if err := json.Unmarshal(record.Data, &profile); err != nil {
			return nil, err
		}
		if profile.ProviderID == providerID {
			profiles = append(profiles, profile)
		}
	}
	return profiles, nil
}

// DeleteProvider removes current inventory as one operation. Historical model
// and connection versions remain available to inspect frozen requests.
func (b *Bridge) DeleteProvider(ctx context.Context, id string, revision int64) error {
	return b.Store.Update(ctx, func(tx *persistence.Tx) error {
		var provider domain.Provider
		if err := tx.Get(ctx, "providers", id, &provider); err != nil {
			return err
		}
		if provider.Revision != revision {
			return domain.Conflict("provider changed; reload it before deleting")
		}
		records, err := tx.List(ctx, "models")
		if err != nil {
			return err
		}
		profiles, err := profilesFor(records, id)
		if err != nil {
			return err
		}
		for _, profile := range profiles {
			if err := tx.Delete(ctx, "models", profile.ID, profile.Revision); err != nil {
				return err
			}
		}
		return tx.Delete(ctx, "providers", id, revision)
	})
}

func (b *Bridge) DeleteProfile(ctx context.Context, id string, revision int64) error {
	return b.Store.Update(ctx, func(tx *persistence.Tx) error {
		return tx.Delete(ctx, "models", id, revision)
	})
}

// RefreshProvider reconciles one complete authenticated inventory. Fetching and
// pagination finish before the transaction, so a failed fetch cannot remove
// models. Concurrent user edits or connection changes reject the whole refresh.
func (b *Bridge) RefreshProvider(ctx context.Context, id string) (RefreshResult, error) {
	result := RefreshResult{}
	var provider domain.Provider
	if err := b.Store.Get(ctx, "providers", id, &provider); err != nil {
		return result, err
	}
	records, err := b.Store.List(ctx, "models")
	if err != nil {
		return result, err
	}
	before, err := profilesFor(records, id)
	if err != nil {
		return result, err
	}
	catalog, err := b.Catalog(ctx, id)
	if err != nil {
		return result, err
	}
	listed := make(map[string]Discovery, len(catalog))
	for _, item := range catalog {
		listed[item.ID] = item
	}
	err = b.Store.Update(ctx, func(tx *persistence.Tx) error {
		var current domain.Provider
		if err := tx.Get(ctx, "providers", id, &current); err != nil {
			return err
		}
		if current.Revision != provider.Revision {
			return domain.Conflict("provider changed during refresh; refresh again")
		}
		records, err := tx.List(ctx, "models")
		if err != nil {
			return err
		}
		after, err := profilesFor(records, id)
		if err != nil {
			return err
		}
		revisions := make(map[string]int64, len(before))
		for _, profile := range before {
			revisions[profile.ID] = profile.Revision
		}
		if len(after) != len(before) {
			return domain.Conflict("models changed during refresh; refresh again")
		}
		for _, profile := range after {
			if revisions[profile.ID] != profile.Revision {
				return domain.Conflict("models changed during refresh; refresh again")
			}
		}
		seen := make(map[string]bool, len(after))
		for _, profile := range after {
			item, exists := listed[profile.ModelID]
			if !exists {
				if err := tx.Delete(ctx, "models", profile.ID, profile.Revision); err != nil {
					return err
				}
				result.Removed++
				continue
			}
			seen[profile.ModelID] = true
			if profile.UserModified {
				result.Unchanged++
				continue
			}
			next := profile
			next.Name, next.Protocol, next.Dialect = item.Name, item.Protocol, item.Dialect
			next.Recommendations = item.Recommendations
			next.RecommendationTarget = item.RecommendationTarget
			if sameCatalogConfiguration(profile, next) {
				result.Unchanged++
				continue
			}
			now := time.Now().UTC()
			next.CatalogAt, next.CatalogSource = &now, item.Source
			next.RecommendationSources, next.Verification = item.RecommendationSources, nil
			clearProfilePresentation(&next)
			if err := tx.Save(ctx, "models", &next.Meta, &next); err != nil {
				return err
			}
			result.Updated++
		}
		for _, item := range catalog {
			if seen[item.ID] {
				continue
			}
			seen[item.ID] = true
			now := time.Now().UTC()
			profile := domain.ModelProfile{
				Meta: domain.Meta{ID: domain.ID("model")}, ProviderID: id,
				ModelID: item.ID, Name: item.Name, Protocol: item.Protocol, Dialect: item.Dialect,
				Recommendations: item.Recommendations, RecommendationTarget: item.RecommendationTarget, Overrides: map[string]json.RawMessage{},
				CatalogAt: &now, CatalogSource: item.Source, RecommendationSources: item.RecommendationSources,
			}
			if err := tx.Save(ctx, "models", &profile.Meta, &profile); err != nil {
				return err
			}
			result.Added++
		}
		return nil
	})
	if err != nil {
		return RefreshResult{}, err
	}
	return result, nil
}

func clearProfilePresentation(profile *domain.ModelProfile) {
	profile.Available, profile.ProviderName, profile.UnavailableReason = nil, "", ""
	profile.ReasoningControls, profile.ReasoningError = nil, ""
}

func sameCatalogConfiguration(a, b domain.ModelProfile) bool {
	left, _ := json.Marshal([]any{a.Name, a.Protocol, a.Dialect, a.Recommendations, a.RecommendationTarget})
	right, _ := json.Marshal([]any{b.Name, b.Protocol, b.Dialect, b.Recommendations, b.RecommendationTarget})
	return string(left) == string(right)
}
