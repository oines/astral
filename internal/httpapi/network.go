package httpapi

import (
	"context"
	"net/http"
	"net/url"

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
type NetworkURLsSetting struct {
	Value      []string `json:"value"`
	SavedValue []string `json:"saved_value"`
	Source     string   `json:"source" enum:"cli,environment,settings,discovery,default"`
	Editable   bool     `json:"editable"`
}
type NetworkBoolSetting struct {
	Value      bool   `json:"value"`
	SavedValue bool   `json:"saved_value"`
	Source     string `json:"source" enum:"cli,environment,settings,server,default"`
	Editable   bool   `json:"editable"`
}
type NetworkAddress struct {
	URL   string `json:"url"`
	Label string `json:"label"`
	Kind  string `json:"kind"`
}
type NetworkSettings struct {
	Listen                NetworkStringSetting    `json:"listen"`
	PublicURLs            NetworkURLsSetting      `json:"public_urls"`
	AllowRemoteManagement NetworkBoolSetting      `json:"allow_remote_management"`
	RestrictHostnames     NetworkBoolSetting      `json:"restrict_hostnames"`
	AllowedHostnames      NetworkHostnamesSetting `json:"allowed_hostnames"`
	Addresses             []NetworkAddress        `json:"addresses"`
	RestartRequired       bool                    `json:"restart_required"`
}
type NetworkPatch struct {
	Listen                *string   `json:"listen,omitempty"`
	PublicURLs            *[]string `json:"public_urls,omitempty"`
	AllowRemoteManagement *bool     `json:"allow_remote_management,omitempty"`
	RestrictHostnames     *bool     `json:"restrict_hostnames,omitempty"`
	AllowedHostnames      *[]string `json:"allowed_hostnames,omitempty"`
}

type NetworkHostnamesSetting struct {
	Value      []string `json:"value"`
	SavedValue []string `json:"saved_value"`
	Source     string   `json:"source" enum:"cli,environment,settings,default"`
	Editable   bool     `json:"editable"`
}

type networkHostnameKey struct{}

// NetworkRequestHostname identifies the Web entry being edited. Machine-local
// recovery has no Web entry and deliberately does not carry this context value.
func NetworkRequestHostname(ctx context.Context) string {
	value, _ := ctx.Value(networkHostnameKey{}).(string)
	return value
}

func (a *API) publicURL() string {
	if a.Config.NetworkSettings != nil {
		values := a.Config.NetworkSettings(context.Background()).PublicURLs.Value
		if len(values) > 0 {
			return values[0]
		}
		return ""
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
		address, err := url.Parse("//" + r.Host)
		if err != nil || address.Hostname() == "" {
			return NetworkSettings{}, domain.Invalid("Invalid management hostname.")
		}
		ctx := context.WithValue(r.Context(), networkHostnameKey{}, address.Hostname())
		return a.Config.SaveNetworkSettings(ctx, input)
	})
}
