package computers

import (
	"encoding/json"
	"io"
	"net/http"
	"time"

	core "github.com/oines/astral-core"
	"github.com/oines/astral-core/standardtools"
	"github.com/oines/astral/internal/domain"
)

type Hello struct {
	BootID       string                `json:"boot_id"`
	Hostname     string                `json:"hostname"`
	OS           string                `json:"os"`
	Arch         string                `json:"arch"`
	Shell        string                `json:"shell"`
	Home         string                `json:"home"`
	Root         string                `json:"root,omitempty"`
	Container    *domain.ContainerInfo `json:"container,omitempty"`
	Capabilities []string              `json:"capabilities,omitempty"`
}
type EnrollmentRequest struct {
	Token string `json:"token"`
	Hello Hello  `json:"hello"`
}
type Credentials struct {
	ServerURL  string `json:"server_url"`
	ComputerID string `json:"computer_id"`
	Token      string `json:"token"`
}
type EnrollmentResult struct {
	Enrollment domain.Enrollment `json:"enrollment"`
	Token      string            `json:"token"`
	Command    string            `json:"command"`
}
type Call struct {
	Binding   domain.ExecutionBinding      `json:"binding"`
	Execution string                       `json:"execution,omitempty"`
	Path      string                       `json:"path,omitempty"`
	ForRead   bool                         `json:"for_read,omitempty"`
	Process   standardtools.ProcessRequest `json:"process,omitempty"`
	ProcessID string                       `json:"process_id,omitempty"`
	Signal    string                       `json:"signal,omitempty"`
	Offset    int64                        `json:"offset,omitempty"`
	Length    *int64                       `json:"length,omitempty"`
	Version   string                       `json:"version,omitempty"`
}
type FileInfo struct {
	Path      string    `json:"path"`
	Name      string    `json:"name"`
	Size      int64     `json:"size"`
	ModTime   time.Time `json:"modified_at"`
	MediaType string    `json:"media_type"`
	Version   string    `json:"version,omitempty"`
}
type GatewayRequest struct {
	Binding domain.ExecutionBinding `json:"binding"`
	Method  string                  `json:"method"`
	Path    string                  `json:"path"`
	Query   string                  `json:"query,omitempty"`
	Body    json.RawMessage         `json:"body,omitempty"`
	Intent  string                  `json:"intent,omitempty"`
}
type GatewayResponse struct {
	Status  int           `json:"status"`
	Headers http.Header   `json:"headers"`
	Body    []byte        `json:"-"`
	Stream  io.ReadCloser `json:"-"`
}
type processStart struct {
	ID string `json:"id"`
}
type processExit struct {
	Code  int    `json:"code"`
	Error string `json:"error,omitempty"`
}

func toolError(err error, outcome core.Outcome) error {
	if err == nil {
		return nil
	}
	return &core.ToolError{Outcome: outcome, Code: "computer_operation", Cause: err}
}
