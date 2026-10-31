package channels

import (
	"context"
	"encoding/json"
	"fmt"
	"mime"
	"net/http"
	"net/url"
	"regexp"
	"strings"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/modelbridge"
)

// QQ has separate private and group address spaces, including when their
// numeric IDs happen to be equal. Keep the address kind in the stored ID.
var qqAddress = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func qqTarget(id string) (kind, platformID string, err error) {
	kind, platformID, ok := strings.Cut(id, ":")
	if !ok || (kind != "private" && kind != "group") || !qqAddress.MatchString(platformID) {
		return "", "", domain.Invalid("QQ chat ID must be private:<user ID> or group:<group ID>")
	}
	return kind, platformID, nil
}

func qqSplitText(text string, limit int) ([]string, error) {
	if strings.TrimSpace(text) == "" {
		return nil, domain.Invalid("message text is empty")
	}
	runes := []rune(text)
	var chunks []string
	for len(runes) > 0 {
		end := min(len(runes), limit)
		if end < len(runes) {
			for i := end; i > end/2; i-- {
				if runes[i-1] == '\n' || runes[i-1] == ' ' {
					end = i
					break
				}
			}
		}
		part := string(runes[:end])
		if strings.TrimSpace(part) != "" {
			chunks = append(chunks, part)
		}
		runes = runes[end:]
	}
	return chunks, nil
}

// WebSocket endpoints follow the same authenticated endpoint policy as HTTP:
// TLS for remote hosts and plaintext only for a loopback deployment.
func qqValidateWebSocket(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "ws" && u.Scheme != "wss" {
		return domain.Invalid("a ws:// or wss:// WebSocket endpoint is required")
	}
	if u.Scheme == "wss" {
		u.Scheme = "https"
	} else {
		u.Scheme = "http"
	}
	return modelbridge.ValidateURL(u.String())
}

func qqHTTPClient(client *http.Client) *http.Client {
	if client == nil {
		client = http.DefaultClient
	}
	owned := *client
	owned.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &owned
}

func qqDownloadURL(ctx context.Context, client *http.Client, raw string, attachment domain.Attachment) (Download, error) {
	if strings.HasPrefix(raw, "//") {
		raw = "https:" + raw
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" || u.Scheme != "https" && u.Scheme != "http" {
		return Download{}, domain.Invalid("the platform did not provide an HTTP attachment URL")
	}
	// Download URLs can contain signed query parameters; never send account
	// authorization headers or follow a redirect to a different source.
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return Download{}, domain.Invalid("invalid attachment URL")
	}
	response, err := client.Do(request)
	if err != nil {
		return Download{}, domain.Offline("attachment download could not start")
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		return Download{}, domain.Offline(fmt.Sprintf("attachment download returned HTTP %d", response.StatusCode))
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil {
		mediaType = ""
	}
	return Download{Body: response.Body, Size: response.ContentLength, Name: attachment.Name, MediaType: mediaType}, nil
}

// OneBot uses both strings and JSON integers for identifiers. Do not round a
// large ID through float64 while normalizing the wire representation.
type qqValue string

func (v *qqValue) UnmarshalJSON(raw []byte) error {
	if string(raw) == "null" {
		*v = ""
		return nil
	}
	if len(raw) > 0 && raw[0] == '"' {
		var text string
		if err := json.Unmarshal(raw, &text); err != nil {
			return err
		}
		*v = qqValue(text)
		return nil
	}
	var number json.Number
	if err := json.Unmarshal(raw, &number); err != nil {
		return err
	}
	if _, err := number.Int64(); err != nil {
		return err
	}
	*v = qqValue(number.String())
	return nil
}
