// Package filestream stores references and transfers bytes directly between a
// source Computer and the consumer. It has no Server filesystem asset directory.
package filestream

import (
	"context"
	"encoding/json"
	"io"
	"net/url"
	"strings"

	core "github.com/oines/astral-core"
	"github.com/oines/astral/internal/computers"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

type Service struct {
	Store *persistence.Store
	Hub   *computers.Hub
}

func URL(ref domain.FileRef) string { return "astral-file://" + ref.ComputerID + "/" + ref.ID }
func Parse(raw string) (computerID, id string, err error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "astral-file" || u.Host == "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return "", "", domain.Invalid("invalid Computer file reference")
	}
	id = strings.TrimPrefix(u.Path, "/")
	if id == "" || strings.Contains(id, "/") {
		return "", "", domain.Invalid("invalid file reference identity")
	}
	return u.Host, id, nil
}

// PathURL names a live file on its Computer without registering server state.
func PathURL(computerID, path string) string {
	locator := url.URL{Scheme: "astral-file", Host: computerID, RawQuery: url.Values{"path": {path}}.Encode()}
	return locator.String()
}

// source resolves a command path on its original binding, or a URI on the
// explicitly named Computer under the caller's current session permissions.
func (s *Service) source(ctx context.Context, b domain.ExecutionBinding, path, uri string) (domain.ExecutionBinding, domain.FileRef, error) {
	ref := domain.FileRef{AgentID: b.AgentID, ComputerID: b.ComputerID, Path: path}
	if uri == "" {
		if b.GrantID == "" || path == "" {
			return b, ref, domain.Invalid("a Computer path requires the original command binding")
		}
		return b, ref, nil
	}
	u, err := url.Parse(uri)
	if err != nil {
		return b, ref, domain.Invalid("invalid Computer file URI")
	}
	if u.RawQuery != "" {
		query, err := url.ParseQuery(u.RawQuery)
		if err != nil || u.Scheme != "astral-file" || u.Host == "" || u.User != nil || u.Path != "" || u.Fragment != "" || len(query) != 1 || len(query["path"]) != 1 || query.Get("path") == "" || strings.ContainsRune(query.Get("path"), 0) {
			return b, ref, domain.Invalid("invalid Computer file path URI")
		}
		ref.ComputerID, ref.Path = u.Host, query.Get("path")
	} else {
		ref, err = s.Get(ctx, b.AgentID, uri)
		if err != nil {
			return b, ref, err
		}
	}
	b, err = s.Hub.Bind(ctx, b.AgentID, b.SessionID, ref.ComputerID, "")
	return b, ref, err
}

func (s *Service) StatSource(ctx context.Context, b domain.ExecutionBinding, path, uri string) (domain.FileRef, error) {
	b, ref, err := s.source(ctx, b, path, uri)
	if err != nil || ref.Version != "" {
		return ref, err
	}
	var info computers.FileInfo
	err = s.Hub.JSON(ctx, "file.stat", computers.Call{Binding: b, Path: ref.Path}, &info)
	ref.Path, ref.DisplayName, ref.MediaType, ref.Size = info.Path, info.Name, info.MediaType, info.Size
	return ref, err
}

func (s *Service) OpenSource(ctx context.Context, b domain.ExecutionBinding, path, uri string) (computers.FileInfo, io.ReadCloser, error) {
	b, ref, err := s.source(ctx, b, path, uri)
	if err != nil {
		return computers.FileInfo{}, nil, err
	}
	info, body, err := s.open(ctx, computers.Call{Binding: b, Path: ref.Path, Version: ref.Version})
	if ref.ID != "" {
		// A stored attachment may have a verified media type that differs from
		// its on-disk extension. The daemon supplies the current byte length.
		info.Name, info.MediaType = ref.DisplayName, ref.MediaType
	}
	return info, body, err
}
func (s *Service) Upload(ctx context.Context, b domain.ExecutionBinding, name string, body io.Reader) (domain.FileRef, error) {
	_, stream, err := s.Hub.Call(ctx, "file.upload", computers.Call{Binding: b, Path: name}, body)
	if err != nil {
		return domain.FileRef{}, err
	}
	defer stream.Close()
	raw, err := io.ReadAll(io.LimitReader(stream, 64<<10))
	if err != nil {
		return domain.FileRef{}, err
	}
	var info computers.FileInfo
	if err = json.Unmarshal(raw, &info); err != nil {
		return domain.FileRef{}, err
	}
	ref := domain.FileRef{Meta: domain.Meta{ID: domain.ID("file")}, AgentID: b.AgentID, ComputerID: b.ComputerID, Path: info.Path,
		DisplayName: info.Name, MediaType: info.MediaType, Size: info.Size, Version: info.Version}
	err = s.Store.Update(ctx, func(tx *persistence.Tx) error {
		if err := tx.RequireActiveAgent(ctx, b.AgentID); err != nil {
			return err
		}
		return tx.Save(ctx, "files", &ref.Meta, &ref)
	})
	return ref, err
}
func (s *Service) Get(ctx context.Context, agentID, raw string) (domain.FileRef, error) {
	computerID, id, err := Parse(raw)
	if err != nil {
		return domain.FileRef{}, err
	}
	var ref domain.FileRef
	if err = s.Store.Get(ctx, "files", id, &ref); err != nil {
		return ref, err
	}
	if ref.AgentID != agentID || ref.ComputerID != computerID {
		return ref, domain.Forbidden("file reference is outside this agent's view")
	}
	return ref, nil
}
func (s *Service) Read(ctx context.Context, agentID, sessionID, raw string, offset int64, length *int64) (domain.FileRef, computers.FileInfo, io.ReadCloser, error) {
	ref, err := s.Get(ctx, agentID, raw)
	if err != nil {
		return ref, computers.FileInfo{}, nil, err
	}
	b, err := s.Hub.Bind(ctx, agentID, sessionID, ref.ComputerID, "")
	if err != nil {
		return ref, computers.FileInfo{}, nil, err
	}
	info, stream, err := s.open(ctx, computers.Call{Binding: b, Path: ref.Path, Version: ref.Version, Offset: offset, Length: length})
	return ref, info, stream, err
}

func (s *Service) open(ctx context.Context, call computers.Call) (computers.FileInfo, io.ReadCloser, error) {
	meta, stream, err := s.Hub.Call(ctx, "file.read", call, nil)
	if err != nil {
		return computers.FileInfo{}, nil, err
	}
	var info computers.FileInfo
	if err := json.Unmarshal(meta, &info); err != nil {
		_ = stream.Close()
		return info, nil, err
	}
	return info, stream, nil
}

// FreezeImages copies the exact native image payload into the Core message.
// Computer references remain for file work, while rollout replay is independent
// of those files and of the Computer's future availability.
func (s *Service) FreezeImages(ctx context.Context, agentID, sessionID string, message *core.Message) error {
	for i := range message.Content {
		part := &message.Content[i]
		if part.Type != "image" || len(part.Data) > 0 {
			continue
		}
		ref, _, stream, err := s.Read(ctx, agentID, sessionID, part.URL, 0, nil)
		if err != nil {
			return err
		}
		if ref.Version == "" {
			_ = stream.Close()
			return domain.Invalid("model images require a source Computer snapshot")
		}
		if !strings.HasPrefix(ref.MediaType, "image/") {
			_ = stream.Close()
			return domain.Invalid("file reference is not an image")
		}
		bytes, err := io.ReadAll(io.LimitReader(stream, (20<<20)+1))
		_ = stream.Close()
		if err != nil {
			return err
		}
		if len(bytes) > 20<<20 {
			return domain.Invalid("image exceeds the 20 MiB model input limit")
		}
		image, err := PrepareImage(ctx, bytes, core.ImageOptions{})
		if err != nil {
			return err
		}
		image.URL = part.URL
		*part = image
	}
	return nil
}

// PrepareImage freezes bytes already available at admission, such as an
// inline attachment, using the same representation as FreezeImages.
func PrepareImage(ctx context.Context, data []byte, options core.ImageOptions) (core.Content, error) {
	processed, err := core.ProcessImage(ctx, data, options)
	if err != nil {
		return core.Content{}, err
	}
	if processed.IsError {
		return core.Content{}, domain.Invalid(processed.Content[0].Text)
	}
	return processed.Content[1], nil
}
