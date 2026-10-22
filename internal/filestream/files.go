// Package filestream stores references and transfers bytes directly between a
// source Computer and the consumer. It has no Server filesystem asset directory.
package filestream

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/url"
	"path"
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

func (s *Service) Reference(ctx context.Context, b domain.ExecutionBinding, source string, snapshot bool) (domain.FileRef, error) {
	method := "file.stat"
	if snapshot {
		method = "file.snapshot"
	}
	var info computers.FileInfo
	if err := s.Hub.JSON(ctx, method, computers.Call{Binding: b, Path: source}, &info); err != nil {
		return domain.FileRef{}, err
	}
	return s.save(ctx, b, info)
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
	return s.save(ctx, b, info)
}
func (s *Service) save(ctx context.Context, b domain.ExecutionBinding, info computers.FileInfo) (domain.FileRef, error) {
	ref := domain.FileRef{Meta: domain.Meta{ID: domain.ID("file")}, AgentID: b.AgentID, ComputerID: b.ComputerID, Path: info.Path,
		DisplayName: info.Name, MediaType: info.MediaType, Size: info.Size, Version: info.Version}
	err := s.Store.Save(ctx, "files", &ref.Meta, &ref)
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
	meta, stream, err := s.Hub.Call(ctx, "file.read", computers.Call{Binding: b, Path: ref.Path, Version: ref.Version, Offset: offset, Length: length}, nil)
	if err != nil {
		return ref, computers.FileInfo{}, nil, err
	}
	var info computers.FileInfo
	if err = json.Unmarshal(meta, &info); err != nil {
		_ = stream.Close()
		return ref, info, nil, err
	}
	return ref, info, stream, nil
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
		processed, err := core.ProcessImage(ctx, bytes, core.ImageOptions{})
		if err != nil {
			return err
		}
		if processed.IsError {
			return domain.Invalid(processed.Content[0].Text)
		}
		image := processed.Content[1]
		image.URL = part.URL
		*part = image
	}
	return nil
}

// PersistToolImages freezes derived image bytes on the invocation's source
// Computer before Core can commit the tool result.
func (s *Service) PersistToolImages(ctx context.Context, b domain.ExecutionBinding, result *core.ToolResult) error {
	for i := range result.Content {
		part := &result.Content[i]
		if part.Type != "image" {
			continue
		}
		if part.URL != "" {
			if _, err := s.Get(ctx, b.AgentID, part.URL); err != nil {
				return err
			}
			if len(part.Data) == 0 {
				message := core.Message{Content: []core.Content{*part}}
				if err := s.FreezeImages(ctx, b.AgentID, b.SessionID, &message); err != nil {
					return err
				}
				*part = message.Content[0]
			}
			continue
		}
		var encoded string
		if json.Unmarshal(part.Data, &encoded) != nil {
			return domain.Invalid("tool image did not contain encoded image bytes")
		}
		suffix := map[string]string{"image/png": ".png", "image/jpeg": ".jpg", "image/webp": ".webp", "image/gif": ".gif"}[part.MediaType]
		if suffix == "" {
			return domain.Invalid("unsupported tool image media type")
		}
		ref, err := s.Upload(ctx, b, path.Base(domain.ID("image")+suffix), base64.NewDecoder(base64.StdEncoding, strings.NewReader(encoded)))
		if err != nil {
			return err
		}
		// Keep the original tool-produced bytes in rollout. The attachment copy
		// provides a URI for file tools, not the replay source.
		part.URL = URL(ref)
	}
	return nil
}
