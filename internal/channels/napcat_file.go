package channels

import (
	"bufio"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/oines/astral/internal/domain"
)

// get_file may return a file-sized Base64 JSON string. Locate that value with
// the JSON decoder, then decode the string directly from the response stream.
// Only the envelope metadata is materialized; status validation also runs at
// EOF, since OneBot may put retcode after data.
func (n *napcat) downloadFile(ctx context.Context, file string, attachment domain.Attachment) (Download, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, n.config.BaseURL+"/get_file", bytes.NewReader(encoded(map[string]string{"file": file})))
	if err != nil {
		return Download{}, err
	}
	request.Header.Set("Content-Type", "application/json")
	if n.token != "" {
		request.Header.Set("Authorization", "Bearer "+n.token)
	}
	response, err := n.client.Do(request)
	if err != nil {
		return Download{}, domain.Offline("NapCat attachment download could not start")
	}
	var captured bytes.Buffer
	decoder := json.NewDecoder(io.TeeReader(response.Body, &captured))
	found, err := findBase64Value(decoder)
	if err != nil {
		response.Body.Close()
		return Download{}, domain.Offline("NapCat returned invalid attachment metadata")
	}
	if !found {
		response.Body.Close()
		return n.downloadFileURL(ctx, response.StatusCode, captured.Bytes(), attachment)
	}
	prefix := append([]byte(nil), captured.Bytes()[:decoder.InputOffset()]...)
	rest := bufio.NewReader(io.MultiReader(decoder.Buffered(), response.Body))
	for _, expected := range []byte{':', '"'} {
		value, readErr := readJSONNonspace(rest)
		if readErr != nil || value != expected {
			response.Body.Close()
			return Download{}, domain.Offline("NapCat returned an invalid Base64 file field")
		}
	}
	stringBody := &jsonBase64String{reader: rest}
	envelope := &napcatFileStream{ReadCloser: response.Body, decoded: base64.NewDecoder(base64.StdEncoding, stringBody), rest: rest, prefix: prefix, status: response.StatusCode}
	// Empty Base64 is metadata-only; let a returned HTTP URL supply the file.
	if peek, _ := rest.Peek(1); len(peek) == 1 && peek[0] == '"' {
		_, _ = rest.ReadByte()
		raw, readErr := envelope.metadata()
		response.Body.Close()
		if readErr != nil {
			return Download{}, readErr
		}
		return n.downloadFileURL(ctx, response.StatusCode, raw, attachment)
	}
	return Download{Body: envelope, Size: attachment.Size, Name: attachment.Name}, nil
}

func findBase64Value(decoder *json.Decoder) (bool, error) {
	token, err := decoder.Token()
	if err != nil {
		return false, err
	}
	delim, compound := token.(json.Delim)
	if !compound {
		return false, nil
	}
	for decoder.More() {
		if delim == '{' {
			key, keyErr := decoder.Token()
			if keyErr != nil {
				return false, keyErr
			}
			if key == "base64" {
				return true, nil
			}
		}
		found, valueErr := findBase64Value(decoder)
		if found || valueErr != nil {
			return found, valueErr
		}
	}
	_, err = decoder.Token()
	return false, err
}

func readJSONNonspace(reader *bufio.Reader) (byte, error) {
	for {
		value, err := reader.ReadByte()
		if err != nil || value != ' ' && value != '\n' && value != '\r' && value != '\t' {
			return value, err
		}
	}
}

type jsonBase64String struct {
	reader *bufio.Reader
	done   bool
}

func (r *jsonBase64String) Read(p []byte) (int, error) {
	if r.done {
		return 0, io.EOF
	}
	for i := range p {
		value, err := r.reader.ReadByte()
		if err != nil {
			return i, io.ErrUnexpectedEOF
		}
		if value == '"' {
			r.done = true
			return i, io.EOF
		}
		if value == '\\' {
			value, err = r.reader.ReadByte()
			if err != nil {
				return i, err
			}
			if value == 'u' {
				var digits [4]byte
				if _, err = io.ReadFull(r.reader, digits[:]); err != nil {
					return i, err
				}
				code, parseErr := strconv.ParseUint(string(digits[:]), 16, 8)
				if parseErr != nil {
					return i, parseErr
				}
				value = byte(code)
			}
		}
		p[i] = value
	}
	return len(p), nil
}

type napcatFileStream struct {
	io.ReadCloser
	decoded io.Reader
	rest    *bufio.Reader
	prefix  []byte
	status  int
	done    bool
}

func (r *napcatFileStream) metadata() ([]byte, error) {
	tail, err := io.ReadAll(r.rest)
	return append(append(r.prefix, []byte(`:""`)...), tail...), err
}

func (r *napcatFileStream) Read(p []byte) (int, error) {
	if r.done {
		return 0, io.EOF
	}
	n, err := r.decoded.Read(p)
	if err == io.EOF {
		r.done = true
		raw, metadataErr := r.metadata()
		if metadataErr != nil {
			return n, metadataErr
		}
		if _, metadataErr = napcatFileMetadata(r.status, raw); metadataErr != nil {
			return n, metadataErr
		}
	}
	return n, err
}

func napcatFileMetadata(status int, raw []byte) (string, error) {
	var envelope struct {
		Status string `json:"status"`
		Code   int    `json:"retcode"`
		Data   struct {
			URL string `json:"url"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &envelope) != nil {
		return "", domain.Offline("NapCat returned invalid attachment metadata")
	}
	if status < 200 || status >= 300 || envelope.Status != "ok" || envelope.Code != 0 {
		return "", &napcatAPIError{Code: envelope.Code, Status: status}
	}
	return envelope.Data.URL, nil
}

func (n *napcat) downloadFileURL(ctx context.Context, status int, raw []byte, attachment domain.Attachment) (Download, error) {
	url, err := napcatFileMetadata(status, raw)
	if err != nil {
		return Download{}, err
	}
	if strings.HasPrefix(url, "https://") || strings.HasPrefix(url, "http://") {
		return qqDownloadURL(ctx, n.client, url, attachment)
	}
	return Download{}, domain.Invalid("NapCat returned a host-local path; enableLocalFile2Url is required to download this attachment")
}
