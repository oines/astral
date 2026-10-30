// Package httpapi is the Owner boundary. It never shares an authentication
// surface with the Agent Gateway or accepts execution identity in request data.
package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	core "github.com/oines/astral-core"
	models "github.com/oines/astral-core/models"

	"github.com/oines/astral/internal/auth"
	"github.com/oines/astral/internal/channels"
	"github.com/oines/astral/internal/computers"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/gateway"
	"github.com/oines/astral/internal/harness"
	"github.com/oines/astral/internal/httpcontract"
	"github.com/oines/astral/internal/modelbridge"
	"github.com/oines/astral/internal/persistence"
	"github.com/oines/astral/internal/search"
	"github.com/oines/astral/internal/serverupdate"
	"github.com/oines/astral/internal/timezone"
)

type Config struct {
	Harness                 *harness.Service
	Channels                *channels.Service
	Auth                    *auth.Service
	PublicURL               string
	DataDirectory           string
	Version                 string
	HostInfo                func(context.Context) domain.HostInfo
	UseLocal                func(context.Context) (domain.ComputerStatus, error)
	CreateContainer         func(context.Context, string) (domain.Provisioning, error)
	NetworkSettings         func(context.Context) NetworkSettings
	SaveNetworkSettings     func(context.Context, NetworkPatch) (NetworkSettings, error)
	ValidateAccessAddresses func(*http.Request, []string) error
	Updates                 serverupdate.Controller
}
type API struct {
	Config       Config
	Contract     *httpcontract.Contract
	mux          *http.ServeMux
	authMux      *http.ServeMux
	authRequests publicAuthRequests
	handler      http.Handler
}
type Preferences struct {
	domain.Meta
	domain.Preferences
}
type Bootstrap struct {
	Auth        auth.View               `json:"auth"`
	Version     string                  `json:"version"`
	PublicURL   string                  `json:"public_url"`
	Network     NetworkSettings         `json:"network"`
	Host        domain.HostInfo         `json:"host"`
	Preferences Preferences             `json:"preferences"`
	Timezone    timezone.View           `json:"timezone"`
	Onboarding  harness.Onboarding      `json:"onboarding"`
	Agents      []domain.Agent          `json:"agents"`
	Sessions    []domain.Session        `json:"sessions"`
	Schedules   []domain.Schedule       `json:"schedules"`
	Computers   []domain.ComputerStatus `json:"computers"`
	Models      []domain.ModelProfile   `json:"models"`
}
type ProviderInput struct {
	domain.Provider
	APIKey  *string            `json:"api_key,omitempty"`
	Headers *map[string]string `json:"headers,omitempty"`
}
type SearchInput struct {
	domain.SearchProvider
	APIKey      *string           `json:"api_key,omitempty"`
	Credentials map[string]string `json:"credentials,omitempty"`
}
type ConnectionInput struct {
	domain.Connection
	Token *string `json:"token,omitempty"`
}
type SessionInput struct {
	AgentID string `json:"agent_id"`
	Title   string `json:"title"`
}
type SessionPresentation struct {
	Title    *string `json:"title,omitempty"`
	Pinned   *bool   `json:"pinned,omitempty"`
	Archived *bool   `json:"archived,omitempty"`
	Private  *bool   `json:"private,omitempty"`
}
type SelectModel struct {
	ProfileID string                 `json:"profile_id"`
	Reasoning *core.ReasoningOptions `json:"reasoning,omitempty"`
}
type SelectComputer struct {
	ComputerID string `json:"computer_id"`
	CWD        string `json:"cwd,omitempty"`
}
type Compact struct {
	Instructions string `json:"instructions,omitempty"`
}
type ClearInputs struct {
	Kind string `json:"kind" enum:"all,steer,follow_up"`
}
type MovePending struct {
	BeforeID string `json:"before_id"`
}
type Fork struct {
	ID      string `json:"id"`
	EntryID string `json:"entry_id"`
	Title   string `json:"title"`
}
type Approval struct {
	Decision core.ApprovalDecision `json:"decision"`
}
type EnrollmentInput struct {
	Container bool   `json:"container"`
	ServerURL string `json:"server_url,omitempty"`
}
type Intent struct {
	ID string `json:"id"`
}
type ComputerUpdate struct {
	Revision int64  `json:"revision"`
	Name     string `json:"name"`
}
type ConversationInput struct {
	ExternalID string `json:"external_id"`
	TopicID    string `json:"topic_id,omitempty"`
}
type BindingInput struct {
	ConversationID string `json:"conversation_id"`
	SessionID      string `json:"session_id,omitempty"`
}
type ProactiveInput struct {
	Revision int64 `json:"revision"`
	Enabled  bool  `json:"enabled"`
}
type SearchQuery = search.Request
type Usage struct {
	Agents   []domain.Agent        `json:"agents"`
	Sessions []domain.Session      `json:"sessions"`
	Profiles []domain.ModelProfile `json:"profiles"`
}
type ContextStats struct {
	EstimatedInput    int    `json:"estimated_input"`
	ContextWindow     int    `json:"context_window"`
	MaxInputTokens    int    `json:"max_input_tokens"`
	OutputReservation int    `json:"output_reservation"`
	HeadID            string `json:"head_id"`
}
type StorageInfo struct {
	Directory     string `json:"directory"`
	DatabaseBytes int64  `json:"database_bytes"`
	RolloutBytes  int64  `json:"rollout_bytes"`
	FileStorage   string `json:"file_storage"`
	MediaStorage  string `json:"media_storage"`
}
type ConnectionView struct {
	domain.Connection
	Status       channels.Status       `json:"status"`
	Capabilities channels.Capabilities `json:"capabilities"`
}

func New(config Config) *API {
	a := &API{Config: config, Contract: httpcontract.New(), mux: http.NewServeMux()}
	a.resources()
	a.sessionRoutes()
	a.scheduleRoutes()
	a.attemptRoutes()
	a.systemRoutes()
	a.networkRoutes()
	a.updateRoutes()
	a.streamRoutes()
	a.authRoutes()
	a.handler = a.authenticate(a.mux)
	return a
}
func (a *API) ServeHTTP(w http.ResponseWriter, r *http.Request) { a.handler.ServeHTTP(w, r) }
func route[Request, Response any](a *API, method, path, id, tag string, handler func(*http.Request, Request) (Response, error), query ...httpcontract.Parameter) {
	httpcontract.Register(a.Contract, a.mux, httpcontract.Operation{Method: method, Path: "/api/v1" + path, ID: id, Tag: tag, Auth: true, Query: query}, func(w http.ResponseWriter, r *http.Request, request Request) (Response, error) {
		return handler(r, request)
	})
}
func readAll[T any](ctx context.Context, store *persistence.Store, kind string) ([]T, error) {
	records, err := store.List(ctx, kind)
	if err != nil {
		return nil, err
	}
	result := make([]T, 0, len(records))
	for _, record := range records {
		var value T
		if err = json.Unmarshal(record.Data, &value); err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	return result, nil
}
func (a *API) preferences(ctx context.Context) (Preferences, error) {
	p := Preferences{Meta: domain.Meta{ID: "owner"}, Preferences: domain.Preferences{Theme: "system", Language: "en", KeepRunning: true, RunningInput: "follow_up"}}
	err := a.Config.Harness.Store.Get(ctx, "preferences", "owner", &p)
	if domain.IsMissing(err) {
		err = nil
	}
	return p, err
}
func (a *API) computers(ctx context.Context) ([]domain.ComputerStatus, error) {
	records, err := readAll[domain.Computer](ctx, a.Config.Harness.Store, "computers")
	if err != nil {
		return nil, err
	}
	result := make([]domain.ComputerStatus, 0, len(records))
	for _, c := range records {
		c.TokenID = ""
		result = append(result, a.Config.Harness.Hub.Status(c))
	}
	return result, nil
}
func (a *API) systemRoutes() {
	h := a.Config.Harness
	route[httpcontract.Empty, Bootstrap](a, "GET", "/bootstrap", "bootstrap", "System", func(r *http.Request, _ httpcontract.Empty) (Bootstrap, error) {
		v := Bootstrap{Version: a.Config.Version, PublicURL: a.publicURL()}
		if a.Config.Auth != nil {
			v.Auth, _ = a.Config.Auth.View(r.Context(), auth.IdentityFromContext(r.Context()))
		}
		if a.Config.NetworkSettings != nil {
			v.Network = a.Config.NetworkSettings(r.Context())
		}
		if a.Config.HostInfo != nil {
			v.Host = a.Config.HostInfo(r.Context())
		}
		var err error
		if v.Onboarding, err = h.Onboarding(r.Context()); err != nil {
			return v, err
		}
		if v.Preferences, err = a.preferences(r.Context()); err != nil {
			return v, err
		}
		if v.Timezone, err = h.Timezone.View(r.Context()); err != nil {
			return v, err
		}
		if v.Agents, err = readAll[domain.Agent](r.Context(), h.Store, "agents"); err != nil {
			return v, err
		}
		if v.Sessions, err = readAll[domain.Session](r.Context(), h.Store, "sessions"); err != nil {
			return v, err
		}
		visibleSessions := v.Sessions[:0]
		for _, session := range v.Sessions {
			if session.Deletion == nil {
				visibleSessions = append(visibleSessions, session)
			}
		}
		v.Sessions = visibleSessions
		if v.Schedules, err = h.Schedules.List(r.Context(), ""); err != nil {
			return v, err
		}
		if v.Computers, err = a.computers(r.Context()); err != nil {
			return v, err
		}
		v.Models, err = h.Models.ListProfiles(r.Context())
		if err != nil {
			return v, err
		}
		return v, err
	})
	route[harness.OnboardingInput, domain.Agent](a, "POST", "/onboarding/complete", "completeOnboarding", "System", func(r *http.Request, input harness.OnboardingInput) (domain.Agent, error) {
		return h.CompleteOnboarding(r.Context(), input)
	})
	route[httpcontract.Empty, Preferences](a, "GET", "/preferences", "getPreferences", "System", func(r *http.Request, _ httpcontract.Empty) (Preferences, error) { return a.preferences(r.Context()) })
	route[Preferences, Preferences](a, "PUT", "/preferences", "savePreferences", "System", func(r *http.Request, p Preferences) (Preferences, error) {
		if p.Theme != "system" && p.Theme != "light" && p.Theme != "dark" {
			return p, domain.Invalid("unknown appearance theme")
		}
		if p.Language != "en" && p.Language != "zh" {
			return p, domain.Invalid("supported languages are English and Chinese")
		}
		if p.RunningInput != "follow_up" && p.RunningInput != "steer" {
			return p, domain.Invalid("unknown running input preference")
		}
		if err := timezone.Validate(p.Timezone); err != nil {
			return p, err
		}
		p.ID = "owner"
		err := h.Store.Save(r.Context(), "preferences", &p.Meta, &p)
		return p, err
	})
	route[httpcontract.Empty, StorageInfo](a, "GET", "/storage", "storageInfo", "System", func(r *http.Request, _ httpcontract.Empty) (StorageInfo, error) {
		value := StorageInfo{Directory: a.Config.DataDirectory, FileStorage: "Work files and original attachments stay on their source Computers.", MediaStorage: "Core rollout retains the actual model image payload independently of original attachments."}
		for _, name := range []string{"astral.db", "astral.db-wal", "astral.db-shm"} {
			if info, err := os.Stat(filepath.Join(a.Config.DataDirectory, name)); err == nil {
				value.DatabaseBytes += info.Size()
			}
		}
		for _, name := range []string{"core.db", "core.db-wal", "core.db-shm"} {
			if info, err := os.Stat(filepath.Join(a.Config.DataDirectory, name)); err == nil {
				value.RolloutBytes += info.Size()
			}
		}
		return value, nil
	})
	route[httpcontract.Empty, Usage](a, "GET", "/resources/{kind}/{id}/usage", "resourceUsage", "System", func(r *http.Request, _ httpcontract.Empty) (Usage, error) {
		return a.usage(r.Context(), r.PathValue("kind"), r.PathValue("id"))
	})
	route[httpcontract.Empty, []domain.ComputerStatus](a, "GET", "/computers", "listComputers", "Computers", func(r *http.Request, _ httpcontract.Empty) ([]domain.ComputerStatus, error) {
		return a.computers(r.Context())
	})
	a.registerComputerMaintenance()
	route[ComputerUpdate, domain.ComputerStatus](a, "PATCH", "/computers/{id}", "updateComputer", "Computers", func(r *http.Request, input ComputerUpdate) (domain.ComputerStatus, error) {
		var c domain.Computer
		if err := h.Store.Get(r.Context(), "computers", r.PathValue("id"), &c); err != nil {
			return domain.ComputerStatus{}, err
		}
		if input.Revision != c.Revision {
			return domain.ComputerStatus{}, domain.Conflict("Computer changed; reload before saving")
		}
		if strings.TrimSpace(input.Name) == "" {
			return domain.ComputerStatus{}, domain.Invalid("Computer name is required")
		}
		c.Name = input.Name
		err := h.Store.Save(r.Context(), "computers", &c.Meta, &c)
		c.TokenID = ""
		return h.Hub.Status(c), err
	})
	route[httpcontract.Empty, httpcontract.OK](a, "POST", "/computers/{id}/revoke", "revokeComputer", "Computers", func(r *http.Request, _ httpcontract.Empty) (httpcontract.OK, error) {
		return httpcontract.OK{OK: true}, h.Hub.Revoke(r.Context(), r.PathValue("id"))
	})
	route[httpcontract.Empty, httpcontract.OK](a, "DELETE", "/computers/{id}", "deleteComputer", "Computers", func(r *http.Request, _ httpcontract.Empty) (httpcontract.OK, error) {
		return httpcontract.OK{OK: true}, h.DeleteComputer(r.Context(), r.PathValue("id"), revision(r))
	}, httpcontract.Parameter{Name: "revision", Type: "integer", Required: true})
	route[EnrollmentInput, computers.EnrollmentResult](a, "POST", "/computers/enrollments", "enrollComputer", "Computers", func(r *http.Request, input EnrollmentInput) (computers.EnrollmentResult, error) {
		server := input.ServerURL
		if server == "" {
			server = a.publicURL()
		}
		return h.Hub.Enroll(r.Context(), server, input.Container, false)
	})
	route[httpcontract.Empty, domain.Enrollment](a, "GET", "/computers/enrollments/{id}", "getEnrollment", "Computers", func(r *http.Request, _ httpcontract.Empty) (domain.Enrollment, error) {
		var e domain.Enrollment
		err := h.Store.Get(r.Context(), "enrollments", r.PathValue("id"), &e)
		e.TokenHash, e.CredentialID = "", ""
		return e, err
	})
	route[httpcontract.Empty, domain.ComputerStatus](a, "POST", "/computers/local", "useLocalComputer", "Computers", func(r *http.Request, _ httpcontract.Empty) (domain.ComputerStatus, error) {
		if a.Config.UseLocal == nil {
			return domain.ComputerStatus{}, domain.Offline("this Server cannot manage a local Daemon")
		}
		return a.Config.UseLocal(r.Context())
	})
	route[Intent, domain.Provisioning](a, "POST", "/computers/containers", "createLocalContainer", "Computers", func(r *http.Request, input Intent) (domain.Provisioning, error) {
		if input.ID == "" {
			return domain.Provisioning{}, domain.Invalid("a stable provisioning request ID is required")
		}
		if a.Config.CreateContainer == nil {
			return domain.Provisioning{}, domain.Offline("this Server cannot manage local containers")
		}
		return a.Config.CreateContainer(r.Context(), input.ID)
	})
	route[httpcontract.Empty, domain.Provisioning](a, "GET", "/computers/operations/{id}", "getComputerOperation", "Computers", func(r *http.Request, _ httpcontract.Empty) (domain.Provisioning, error) {
		var value domain.Provisioning
		err := h.Store.Get(r.Context(), "provisioning", r.PathValue("id"), &value)
		return value, err
	})
	route[httpcontract.Empty, domain.HostInfo](a, "GET", "/computers/host", "computerHost", "Computers", func(r *http.Request, _ httpcontract.Empty) (domain.HostInfo, error) {
		if a.Config.HostInfo == nil {
			return domain.HostInfo{}, nil
		}
		return a.Config.HostInfo(r.Context()), nil
	})
}
func (a *API) usage(ctx context.Context, kind, id string) (Usage, error) {
	store := a.Config.Harness.Store
	value := Usage{Agents: []domain.Agent{}, Sessions: []domain.Session{}, Profiles: []domain.ModelProfile{}}
	agents, err := readAll[domain.Agent](ctx, store, "agents")
	if err != nil {
		return value, err
	}
	profiles, err := readAll[domain.ModelProfile](ctx, store, "models")
	if err != nil {
		return value, err
	}
	modelIDs := map[string]bool{id: true}
	for _, p := range profiles {
		if kind == "providers" && p.ProviderID == id {
			value.Profiles = append(value.Profiles, p)
			modelIDs[p.ID] = true
		}
	}
	for _, agent := range agents {
		used := kind == "search" && agent.SearchProviderID == id || (kind == "models" || kind == "providers") && (modelIDs[agent.DefaultModelID] || modelIDs[agent.ProbeModelID])
		if kind == "computers" {
			for _, computer := range agent.AllowedComputers {
				if computer == id {
					used = true
				}
			}
		}
		if used {
			value.Agents = append(value.Agents, agent)
		}
	}
	all, err := readAll[domain.Session](ctx, store, "sessions")
	if err != nil {
		return value, err
	}
	for _, session := range all {
		selected := core.ModelSelection{}
		if kind == "models" || kind == "providers" {
			selected, _ = a.Config.Harness.ModelSelection(ctx, session.ID)
		}
		if (kind == "models" || kind == "providers") && modelIDs[selected.ID] || kind == "computers" && session.ComputerID == id || kind == "agents" && session.AgentID == id {
			value.Sessions = append(value.Sessions, session)
		}
	}
	return value, nil
}
func (a *API) resources() {
	h, c := a.Config.Harness, a.Config.Channels
	route[httpcontract.Empty, []domain.Agent](a, "GET", "/agents", "listAgents", "Agents", func(r *http.Request, _ httpcontract.Empty) ([]domain.Agent, error) {
		return readAll[domain.Agent](r.Context(), h.Store, "agents")
	})
	route[domain.Agent, domain.Agent](a, "POST", "/agents", "saveAgent", "Agents", func(r *http.Request, input domain.Agent) (domain.Agent, error) {
		err := h.SaveAgent(r.Context(), &input)
		return input, err
	})
	route[httpcontract.Empty, httpcontract.OK](a, "DELETE", "/agents/{id}", "deleteAgent", "Agents", func(r *http.Request, _ httpcontract.Empty) (httpcontract.OK, error) {
		return httpcontract.OK{OK: true}, h.DeleteAgent(r.Context(), r.PathValue("id"), revision(r))
	}, httpcontract.Parameter{Name: "revision", Type: "integer", Required: true})
	route[httpcontract.Empty, []domain.Provider](a, "GET", "/providers", "listProviders", "Models", func(r *http.Request, _ httpcontract.Empty) ([]domain.Provider, error) {
		return readAll[domain.Provider](r.Context(), h.Store, "providers")
	})
	route[httpcontract.Empty, modelbridge.Authorization](a, "POST", "/providers/{id}/authorize", "authorizeProvider", "Models", func(r *http.Request, _ httpcontract.Empty) (modelbridge.Authorization, error) {
		return h.Models.StartAuthorization(r.Context(), r.PathValue("id"))
	})
	route[httpcontract.Empty, modelbridge.Authorization](a, "GET", "/provider-authorizations/{id}", "providerAuthorization", "Models", func(r *http.Request, _ httpcontract.Empty) (modelbridge.Authorization, error) {
		return h.Models.PollAuthorization(r.Context(), r.PathValue("id"))
	})
	route[httpcontract.Empty, []modelbridge.ProviderPreset](a, "GET", "/provider-presets", "listProviderPresets", "Models", func(_ *http.Request, _ httpcontract.Empty) ([]modelbridge.ProviderPreset, error) {
		return modelbridge.ProviderPresets(), nil
	})
	route[httpcontract.Empty, []models.ReasoningFormat](a, "GET", "/reasoning-formats", "reasoningFormats", "Models", func(r *http.Request, _ httpcontract.Empty) ([]models.ReasoningFormat, error) {
		return models.ReasoningFormats(r.URL.Query().Get("protocol")), nil
	}, httpcontract.Parameter{Name: "protocol", Type: "string"})
	route[httpcontract.Empty, []modelbridge.Discovery](a, "GET", "/provider-presets/{id}/models", "presetModels", "Models", func(r *http.Request, _ httpcontract.Empty) ([]modelbridge.Discovery, error) {
		return h.Models.PresetCatalog(r.Context(), r.PathValue("id"))
	})
	route[httpcontract.Empty, modelbridge.RefreshResult](a, "POST", "/providers/{id}/refresh", "refreshProviderModels", "Models", func(r *http.Request, _ httpcontract.Empty) (modelbridge.RefreshResult, error) {
		return h.Models.RefreshProvider(r.Context(), r.PathValue("id"))
	})
	route[ProviderInput, domain.Provider](a, "POST", "/providers", "saveProvider", "Models", func(r *http.Request, input ProviderInput) (domain.Provider, error) {
		err := h.Models.SaveProvider(r.Context(), &input.Provider, input.APIKey, input.Headers)
		return input.Provider, err
	})
	route[httpcontract.Empty, []modelbridge.Discovery](a, "GET", "/providers/{id}/catalog", "modelCatalog", "Models", func(r *http.Request, _ httpcontract.Empty) ([]modelbridge.Discovery, error) {
		return h.Models.Catalog(r.Context(), r.PathValue("id"))
	})
	route[httpcontract.Empty, modelbridge.Discovery](a, "GET", "/providers/{id}/model-recommendation", "modelRecommendation", "Models", func(r *http.Request, _ httpcontract.Empty) (modelbridge.Discovery, error) {
		return h.Models.ModelRecommendation(r.Context(), r.PathValue("id"), r.URL.Query().Get("model_id"))
	}, httpcontract.Parameter{Name: "model_id", Type: "string", Required: true})
	route[httpcontract.Empty, []domain.ModelProfile](a, "GET", "/models", "listModels", "Models", func(r *http.Request, _ httpcontract.Empty) ([]domain.ModelProfile, error) {
		return h.Models.ListProfiles(r.Context())
	})
	route[domain.ModelProfile, domain.ModelProfile](a, "POST", "/models/describe", "describeModel", "Models", func(r *http.Request, input domain.ModelProfile) (domain.ModelProfile, error) {
		return h.Models.PreviewProfile(r.Context(), input), nil
	})
	route[domain.ModelProfile, domain.ModelProfile](a, "POST", "/models", "saveModel", "Models", func(r *http.Request, input domain.ModelProfile) (domain.ModelProfile, error) {
		err := h.Models.SaveProfile(r.Context(), &input)
		if err != nil {
			return input, err
		}
		return h.Models.DescribeProfile(r.Context(), input), nil
	})
	route[httpcontract.Empty, domain.ModelVerification](a, "POST", "/models/{id}/verify", "verifyModel", "Models", func(r *http.Request, _ httpcontract.Empty) (domain.ModelVerification, error) {
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Minute)
		defer cancel()
		return h.Models.Verify(ctx, r.PathValue("id"))
	})
	route[httpcontract.Empty, []search.Adapter](a, "GET", "/search-adapters", "listSearchAdapters", "Search", func(_ *http.Request, _ httpcontract.Empty) ([]search.Adapter, error) {
		return search.Adapters(), nil
	})
	route[httpcontract.Empty, []domain.SearchProvider](a, "GET", "/search-providers", "listSearchProviders", "Search", func(r *http.Request, _ httpcontract.Empty) ([]domain.SearchProvider, error) {
		return readAll[domain.SearchProvider](r.Context(), h.Store, "search")
	})
	route[SearchInput, domain.SearchProvider](a, "POST", "/search-providers", "saveSearchProvider", "Search", func(r *http.Request, input SearchInput) (domain.SearchProvider, error) {
		if input.APIKey != nil {
			if input.Credentials == nil {
				input.Credentials = map[string]string{}
			}
			if value, ok := input.Credentials["api_key"]; ok && value != *input.APIKey {
				return domain.SearchProvider{}, domain.Invalid("conflicting api_key credentials")
			}
			input.Credentials["api_key"] = *input.APIKey
		}
		err := h.Search.Save(r.Context(), &input.SearchProvider, input.Credentials)
		return input.SearchProvider, err
	})
	route[SearchQuery, search.Response](a, "POST", "/search-providers/{id}/test", "testSearchProvider", "Search", func(r *http.Request, input SearchQuery) (search.Response, error) {
		return h.Search.Query(r.Context(), r.PathValue("id"), input)
	})
	route[channels.WeixinLoginInput, channels.WeixinLoginView](a, "POST", "/agents/{id}/weixin/login", "startWeixinLogin", "Channels", func(r *http.Request, input channels.WeixinLoginInput) (channels.WeixinLoginView, error) {
		return c.StartWeixinLogin(r.Context(), r.PathValue("id"), input)
	})
	route[channels.WeixinLoginPoll, channels.WeixinLoginView](a, "POST", "/weixin/logins/{id}/poll", "pollWeixinLogin", "Channels", func(r *http.Request, input channels.WeixinLoginPoll) (channels.WeixinLoginView, error) {
		return c.PollWeixinLogin(r.Context(), r.PathValue("id"), input)
	})
	route[httpcontract.Empty, []ConnectionView](a, "GET", "/connections", "listConnections", "Channels", func(r *http.Request, _ httpcontract.Empty) ([]ConnectionView, error) {
		all, err := readAll[domain.Connection](r.Context(), h.Store, "connections")
		if err != nil {
			return nil, err
		}
		result := []ConnectionView{}
		for _, item := range all {
			caps, _ := c.Capabilities(r.Context(), item.ID)
			result = append(result, ConnectionView{Connection: item, Status: c.Status(item.ID), Capabilities: caps})
		}
		return result, nil
	})
	route[ConnectionInput, domain.Connection](a, "POST", "/connections", "saveConnection", "Channels", func(r *http.Request, input ConnectionInput) (domain.Connection, error) {
		err := c.SaveConnection(r.Context(), &input.Connection, input.Token)
		return input.Connection, err
	})
	route[httpcontract.Empty, httpcontract.OK](a, "DELETE", "/connections/{id}", "deleteConnection", "Channels", func(r *http.Request, _ httpcontract.Empty) (httpcontract.OK, error) {
		return httpcontract.OK{OK: true}, c.DeleteConnection(r.Context(), r.PathValue("id"), revision(r))
	}, httpcontract.Parameter{Name: "revision", Type: "integer", Required: true})
	route[ConversationInput, domain.Conversation](a, "POST", "/connections/{id}/conversations", "resolveConversation", "Channels", func(r *http.Request, input ConversationInput) (domain.Conversation, error) {
		return c.ResolveConversation(r.Context(), r.PathValue("id"), input.ExternalID, input.TopicID)
	})
	route[httpcontract.Empty, []domain.Conversation](a, "GET", "/conversations", "listConversations", "Channels", func(r *http.Request, _ httpcontract.Empty) ([]domain.Conversation, error) {
		return readAll[domain.Conversation](r.Context(), h.Store, "conversations")
	})
	route[httpcontract.Empty, []domain.ConversationBinding](a, "GET", "/bindings", "listBindings", "Channels", func(r *http.Request, _ httpcontract.Empty) ([]domain.ConversationBinding, error) {
		return readAll[domain.ConversationBinding](r.Context(), h.Store, "bindings")
	})
	route[BindingInput, domain.ConversationBinding](a, "POST", "/bindings", "bindConversation", "Channels", func(r *http.Request, input BindingInput) (domain.ConversationBinding, error) {
		return c.Bind(r.Context(), input.ConversationID, input.SessionID)
	})
	route[ProactiveInput, domain.Conversation](a, "PATCH", "/conversations/{id}/proactive", "setConversationProactive", "Channels", func(r *http.Request, input ProactiveInput) (domain.Conversation, error) {
		return c.SetProactive(r.Context(), r.PathValue("id"), input.Revision, input.Enabled)
	})
	route[httpcontract.Empty, httpcontract.OK](a, "DELETE", "/bindings/{id}", "unbindConversation", "Channels", func(r *http.Request, _ httpcontract.Empty) (httpcontract.OK, error) {
		return httpcontract.OK{OK: true}, c.Unbind(r.Context(), r.PathValue("id"), revision(r))
	}, httpcontract.Parameter{Name: "revision", Type: "integer", Required: true})
	route[httpcontract.Empty, gateway.MessagePage](a, "GET", "/conversations/{id}/messages", "conversationMessages", "Channels", func(r *http.Request, _ httpcontract.Empty) (gateway.MessagePage, error) {
		after, _ := strconv.ParseInt(r.URL.Query().Get("after"), 10, 64)
		all, err := h.Store.Observations(r.Context(), r.PathValue("id"), after, limit(r, 100))
		next := after
		if len(all) > 0 {
			next = all[len(all)-1].Sequence
		}
		items := make([]gateway.StoredMessageObservationView, 0, len(all))
		for _, item := range all {
			items = append(items, gateway.StoredMessageObservationView{Sequence: item.Sequence, Observation: gateway.MessageObservation(item.Observation)})
		}
		return gateway.MessagePage{Items: items, NextSequence: next}, err
	}, httpcontract.Parameter{Name: "after", Type: "integer"}, httpcontract.Parameter{Name: "limit", Type: "integer"})
	route[httpcontract.Empty, channels.DeliveryView](a, "GET", "/observations/{id}/delivery", "observationDelivery", "Channels", func(r *http.Request, _ httpcontract.Empty) (channels.DeliveryView, error) {
		return c.Delivery(r.Context(), r.PathValue("id"))
	})
	route[httpcontract.Empty, []domain.Outbox](a, "GET", "/outbox", "listOutbox", "Channels", func(r *http.Request, _ httpcontract.Empty) ([]domain.Outbox, error) {
		all, err := readAll[domain.Outbox](r.Context(), h.Store, "outbox")
		if err != nil {
			return nil, err
		}
		result := []domain.Outbox{}
		for _, value := range all {
			if id := r.URL.Query().Get("session_id"); id != "" && value.SessionID != id {
				continue
			}
			result = append(result, value)
		}
		return result, nil
	}, httpcontract.Parameter{Name: "session_id"})
	route[httpcontract.Empty, httpcontract.OK](a, "DELETE", "/providers/{id}", "deleteProvider", "Models", func(r *http.Request, _ httpcontract.Empty) (httpcontract.OK, error) {
		return httpcontract.OK{OK: true}, h.Models.DeleteProvider(r.Context(), r.PathValue("id"), revision(r))
	}, httpcontract.Parameter{Name: "revision", Type: "integer", Required: true})
	route[httpcontract.Empty, httpcontract.OK](a, "DELETE", "/models/{id}", "deleteModel", "Models", func(r *http.Request, _ httpcontract.Empty) (httpcontract.OK, error) {
		return httpcontract.OK{OK: true}, h.Models.DeleteProfile(r.Context(), r.PathValue("id"), revision(r))
	}, httpcontract.Parameter{Name: "revision", Type: "integer", Required: true})
	route[httpcontract.Empty, httpcontract.OK](a, "DELETE", "/search-providers/{id}", "deleteSearchProvider", "Resources", func(r *http.Request, _ httpcontract.Empty) (httpcontract.OK, error) {
		err := h.Store.Update(r.Context(), func(tx *persistence.Tx) error {
			return tx.Delete(r.Context(), "search", r.PathValue("id"), revision(r))
		})
		return httpcontract.OK{OK: true}, err
	}, httpcontract.Parameter{Name: "revision", Type: "integer", Required: true})
}
func (a *API) sessionRoutes() {
	h := a.Config.Harness
	route[httpcontract.Empty, []harness.View](a, "GET", "/sessions", "listSessions", "Sessions", func(r *http.Request, _ httpcontract.Empty) ([]harness.View, error) {
		records, err := readAll[domain.Session](r.Context(), h.Store, "sessions")
		if err != nil {
			return nil, err
		}
		views := make([]harness.View, 0, len(records))
		for _, record := range records {
			if record.Deletion != nil {
				continue
			}
			view, viewErr := h.View(r.Context(), record.ID)
			if viewErr != nil {
				// One unavailable historical configuration must not prevent
				// the Owner from reaching their other Sessions or Settings.
				view = harness.View{Session: record, Status: "needs_attention",
					Error: "This Session could not be opened. Check its Agent and model configuration."}
			}
			views = append(views, view)
		}
		return views, nil
	})
	route[SessionInput, harness.View](a, "POST", "/sessions", "createSession", "Sessions", func(r *http.Request, input SessionInput) (harness.View, error) {
		record, err := h.CreateSession(r.Context(), input.AgentID, input.Title)
		if err != nil {
			return harness.View{}, err
		}
		return h.View(r.Context(), record.ID)
	})
	route[httpcontract.Empty, harness.View](a, "GET", "/sessions/{id}", "getSession", "Sessions", func(r *http.Request, _ httpcontract.Empty) (harness.View, error) {
		return h.View(r.Context(), r.PathValue("id"))
	})
	route[SessionPresentation, httpcontract.OK](a, "PATCH", "/sessions/{id}", "updateSession", "Sessions", func(r *http.Request, input SessionPresentation) (httpcontract.OK, error) {
		return httpcontract.OK{OK: true}, h.SaveSessionPresentation(r.Context(), r.PathValue("id"), input.Title, input.Pinned, input.Archived, input.Private)
	})
	route[httpcontract.Empty, httpcontract.OK](a, "DELETE", "/sessions/{id}", "deleteSession", "Sessions", func(r *http.Request, _ httpcontract.Empty) (httpcontract.OK, error) {
		return httpcontract.OK{OK: true}, h.DeleteSession(r.Context(), r.PathValue("id"), revision(r))
	}, httpcontract.Parameter{Name: "revision", Type: "integer", Required: true})
	route[harness.InputRequest, harness.InputReceipt](a, "POST", "/sessions/{id}/inputs", "submitInput", "Sessions", func(r *http.Request, input harness.InputRequest) (harness.InputReceipt, error) {
		return h.Submit(r.Context(), r.PathValue("id"), input)
	})
	route[httpcontract.Empty, harness.InputReceipt](a, "GET", "/sessions/{id}/inputs/{input}", "inputReceipt", "Sessions", func(r *http.Request, _ httpcontract.Empty) (harness.InputReceipt, error) {
		return h.Receipt(r.Context(), r.PathValue("id"), r.PathValue("input"))
	})
	route[httpcontract.Empty, httpcontract.OK](a, "POST", "/sessions/{id}/stop", "stopSession", "Sessions", func(r *http.Request, _ httpcontract.Empty) (httpcontract.OK, error) {
		return httpcontract.OK{OK: true}, h.Stop(r.Context(), r.PathValue("id"))
	})
	route[httpcontract.Empty, httpcontract.OK](a, "POST", "/sessions/{id}/continue", "continueSession", "Sessions", func(r *http.Request, _ httpcontract.Empty) (httpcontract.OK, error) {
		return httpcontract.OK{OK: true}, h.Continue(r.Context(), r.PathValue("id"))
	})
	route[Compact, httpcontract.OK](a, "POST", "/sessions/{id}/compact", "compactSession", "Sessions", func(r *http.Request, input Compact) (httpcontract.OK, error) {
		return httpcontract.OK{OK: true}, h.Compact(r.Context(), r.PathValue("id"), input.Instructions)
	})
	route[SelectComputer, httpcontract.OK](a, "POST", "/sessions/{id}/computer", "selectComputer", "Sessions", func(r *http.Request, input SelectComputer) (httpcontract.OK, error) {
		return httpcontract.OK{OK: true}, h.SelectComputer(r.Context(), r.PathValue("id"), input.ComputerID, input.CWD)
	})
	route[SelectModel, httpcontract.OK](a, "POST", "/sessions/{id}/model", "selectModel", "Sessions", func(r *http.Request, input SelectModel) (httpcontract.OK, error) {
		return httpcontract.OK{OK: true}, h.SelectModel(r.Context(), r.PathValue("id"), input.ProfileID, input.Reasoning)
	})
	route[ClearInputs, httpcontract.OK](a, "POST", "/sessions/{id}/pending/clear", "clearPending", "Sessions", func(r *http.Request, input ClearInputs) (httpcontract.OK, error) {
		return httpcontract.OK{OK: true}, h.ClearPending(r.Context(), r.PathValue("id"), input.Kind)
	})
	route[harness.PendingEdit, httpcontract.OK](a, "PATCH", "/sessions/{id}/pending/{input}", "updatePending", "Sessions", func(r *http.Request, input harness.PendingEdit) (httpcontract.OK, error) {
		return httpcontract.OK{OK: true}, h.UpdatePending(r.Context(), r.PathValue("id"), r.PathValue("input"), input)
	})
	route[httpcontract.Empty, httpcontract.OK](a, "DELETE", "/sessions/{id}/pending/{input}", "removePending", "Sessions", func(r *http.Request, _ httpcontract.Empty) (httpcontract.OK, error) {
		return httpcontract.OK{OK: true}, h.RemovePending(r.Context(), r.PathValue("id"), r.PathValue("input"))
	})
	route[MovePending, httpcontract.OK](a, "POST", "/sessions/{id}/pending/{input}/move", "movePending", "Sessions", func(r *http.Request, input MovePending) (httpcontract.OK, error) {
		return httpcontract.OK{OK: true}, h.MovePending(r.Context(), r.PathValue("id"), r.PathValue("input"), input.BeforeID)
	})
	route[Fork, domain.Session](a, "POST", "/sessions/{id}/fork", "forkSession", "Sessions", func(r *http.Request, input Fork) (domain.Session, error) {
		return h.Fork(r.Context(), r.PathValue("id"), input.EntryID, input.Title, input.ID)
	})
	route[Approval, httpcontract.OK](a, "POST", "/sessions/{id}/approvals/{approval}", "decideApproval", "Sessions", func(r *http.Request, input Approval) (httpcontract.OK, error) {
		return httpcontract.OK{OK: true}, h.DecideApproval(r.Context(), r.PathValue("id"), r.PathValue("approval"), input.Decision)
	})
	route[httpcontract.Empty, ContextStats](a, "GET", "/sessions/{id}/context", "sessionContext", "Sessions", func(r *http.Request, _ httpcontract.Empty) (ContextStats, error) {
		session, err := h.Core(r.Context(), r.PathValue("id"))
		if err != nil {
			return ContextStats{}, err
		}
		status, err := h.View(r.Context(), r.PathValue("id"))
		if err != nil {
			return ContextStats{}, err
		}
		// During a Run, report the actual frozen request. A model selection for
		// the next Attempt must not change the context ring of the current one.
		if status.RunID != "" {
			page, readErr := session.Attempts().Scan(r.Context(), core.AttemptQuery{Reverse: true, Limit: 1, MaxBytes: 128 << 20})
			if readErr != nil {
				return ContextStats{}, readErr
			}
			if len(page.Attempts) > 0 && page.Attempts[0].RunID == status.RunID {
				attempt := page.Attempts[0]
				request, rebuildErr := session.Attempts().Request(r.Context(), attempt.ID)
				if rebuildErr != nil {
					return ContextStats{}, rebuildErr
				}
				model, resolveErr := h.Models.RestoreModel(r.Context(), core.ModelSnapshot{Spec: attempt.Input.Model, Data: attempt.Input.Snapshot})
				if resolveErr != nil {
					return ContextStats{}, resolveErr
				}
				estimated, estimateErr := modelbridge.EstimateTokens(r.Context(), model, request)
				spec := attempt.Input.Model
				return ContextStats{EstimatedInput: estimated, ContextWindow: spec.ContextWindow, MaxInputTokens: spec.MaxInputTokens, OutputReservation: request.MaxOutputTokens, HeadID: attempt.Input.HeadID}, estimateErr
			}
		}
		view, err := h.InspectContext(r.Context(), r.PathValue("id"))
		if err != nil {
			return ContextStats{}, err
		}
		spec, err := h.Models.ReadSpec(r.Context(), view.Request.ModelID)
		if err != nil {
			return ContextStats{}, err
		}
		return ContextStats{EstimatedInput: view.EstimatedInput, ContextWindow: spec.ContextWindow, MaxInputTokens: spec.MaxInputTokens, OutputReservation: view.Request.MaxOutputTokens, HeadID: view.HeadID}, nil

	})
	route[httpcontract.Empty, gateway.HistoryPage](a, "GET", "/sessions/{id}/history", "sessionHistory", "Sessions", func(r *http.Request, _ httpcontract.Empty) (gateway.HistoryPage, error) {
		return gateway.History(r.Context(), h, r.PathValue("id"), r.URL.Query().Get("cursor"), limit(r, 50), r.URL.Query().Get("trace") == "true", false)
	}, httpcontract.Parameter{Name: "cursor"}, httpcontract.Parameter{Name: "limit", Type: "integer"}, httpcontract.Parameter{Name: "trace", Type: "boolean"})
	route[httpcontract.Empty, core.Entry](a, "GET", "/sessions/{id}/entries/{entry}", "sessionEntry", "Sessions", func(r *http.Request, _ httpcontract.Empty) (core.Entry, error) {
		session, err := h.Core(r.Context(), r.PathValue("id"))
		if err != nil {
			return core.Entry{}, err
		}
		entry, err := session.History().Get(r.Context(), r.PathValue("entry"))
		gateway.RemoveImageData(reflect.ValueOf(&entry))
		return entry, err
	})
}
func revision(r *http.Request) int64 {
	v, _ := strconv.ParseInt(r.URL.Query().Get("revision"), 10, 64)
	return v
}
func limit(r *http.Request, fallback int) int {
	v, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if v <= 0 {
		return fallback
	}
	return min(v, 100)
}
func (a *API) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		if r.Header.Get("X-Request-ID") == "" {
			r.Header.Set("X-Request-ID", domain.ID("request"))
		}
		w.Header().Set("X-Request-ID", r.Header.Get("X-Request-ID"))
		if a.Config.Auth == nil {
			httpcontract.JSON(w, 401, httpcontract.Error{Code: "unauthorized", Message: "Sign in to this Server."})
			return
		}
		if err := a.Config.Auth.CheckOrigin(r); err != nil {
			httpcontract.Failure(w, r, err)
			return
		}
		identity, err := a.Config.Auth.Authenticate(r)
		if err != nil {
			httpcontract.Failure(w, r, err)
			return
		}
		ctx, release := a.Config.Auth.Bind(r.Context(), identity)
		defer release()
		if ctx.Err() != nil {
			httpcontract.JSON(w, 401, httpcontract.Error{Code: "unauthorized", Message: "This session has ended."})
			return
		}
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// Contract is constructed from runtime route declarations without touching
// external resources. The generated file is reproducible and contains no keys.
func Contract() *httpcontract.Contract {
	a := New(Config{})
	a.Contract.Merge(gateway.New(nil, nil).Contract)
	return a.Contract
}
