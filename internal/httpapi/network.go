package httpapi

import (
	"context"
	"net/http"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/httpcontract"
)

// Network settings report effective values separately from the owner's saved
// preferences. A process override never rewrites a lower-priority preference.
type NetworkStringSetting struct {
	Value      string `json:"value"`
	SavedValue string `json:"saved_value"`
	Source     string `json:"source" enum:"cli,environment,settings,discovery,default"`
	Editable   bool   `json:"editable"`
}
type NetworkBoolSetting struct {
	Value      bool   `json:"value"`
	SavedValue bool   `json:"saved_value"`
	Source     string `json:"source" enum:"cli,environment,settings,default"`
	Editable   bool   `json:"editable"`
}
type NetworkAddress struct {
	URL   string `json:"url"`
	Label string `json:"label"`
	Kind  string `json:"kind"`
}
type NetworkSettings struct {
	Listen                NetworkStringSetting `json:"listen"`
	PublicURL             NetworkStringSetting `json:"public_url"`
	AllowRemoteManagement NetworkBoolSetting   `json:"allow_remote_management"`
	Addresses             []NetworkAddress     `json:"addresses"`
	RestartRequired       bool                 `json:"restart_required"`
}
type NetworkPatch struct {
	Listen                *string `json:"listen,omitempty"`
	PublicURL             *string `json:"public_url,omitempty"`
	AllowRemoteManagement *bool   `json:"allow_remote_management,omitempty"`
}

func (a *API) publicURL() string {
	if a.Config.NetworkSettings != nil {
		return a.Config.NetworkSettings(context.Background()).PublicURL.Value
	}
	return a.Config.PublicURL
}
func (a *API) networkRoutes() {
	route[httpcontract.Empty, NetworkSettings](a, "GET", "/settings/network", "getNetworkSettings", "System", func(r *http.Request, _ httpcontract.Empty) (NetworkSettings, error) {
		if a.Config.NetworkSettings == nil {
			return NetworkSettings{}, domain.Offline("Network settings are unavailable.")
		}
		return a.Config.NetworkSettings(r.Context()), nil
	})
	route[NetworkPatch, NetworkSettings](a, "PATCH", "/settings/network", "saveNetworkSettings", "System", func(r *http.Request, input NetworkPatch) (NetworkSettings, error) {
		if a.Config.SaveNetworkSettings == nil {
			return NetworkSettings{}, domain.Offline("Network settings are unavailable.")
		}
		return a.Config.SaveNetworkSettings(r.Context(), input)
	})
}
