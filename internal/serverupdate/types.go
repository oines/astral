// Package serverupdate owns managed Server update control, independently of an
// individual browser request or the Server process being replaced.
package serverupdate

import "context"

type State struct {
	Phase            string  `json:"phase" enum:"idle,checking,current,available,downloading,installing,error,unavailable"`
	CurrentVersion   string  `json:"current_version"`
	Version          string  `json:"version,omitempty"`
	Percent          float64 `json:"percent,omitempty"`
	Error            string  `json:"error,omitempty"`
	Retry            string  `json:"retry,omitempty" enum:"check,update"`
	Unavailable      string  `json:"unavailable,omitempty"`
	Manager          string  `json:"manager" enum:"desktop,systemd-user,manual"`
	OperationID      string  `json:"operation_id,omitempty"`
	OperationState   string  `json:"operation_state,omitempty" enum:"running,complete,failed"`
	OperationVersion string  `json:"operation_version,omitempty"`
}

type Controller interface {
	State(context.Context) (State, error)
	Check(context.Context) (State, error)
	Apply(context.Context) (State, error)
}

// CacheCleanup is an optional capability of the private Desktop controller.
// Tokens identify main-process verified files; no caller supplies a path.
type CacheCleanup interface {
	CacheInventory(context.Context) ([]CacheItem, error)
	CleanCache(context.Context, string, string) (int64, error)
}

type CacheItem struct {
	Token     string `json:"token"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Bytes     int64  `json:"bytes"`
	Protected bool   `json:"protected"`
	Reason    string `json:"reason,omitempty"`
}

type Unmanaged struct{ Version string }

func (u Unmanaged) State(context.Context) (State, error) {
	return State{Phase: "unavailable", CurrentVersion: u.Version, Manager: "manual", Unavailable: "unmanaged"}, nil
}
func (u Unmanaged) Check(ctx context.Context) (State, error) { return u.State(ctx) }
func (u Unmanaged) Apply(ctx context.Context) (State, error) { return u.State(ctx) }
