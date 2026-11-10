package skills

import (
	"bytes"
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sync"

	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/persistence"
)

const (
	Builtin = "builtin"
	Shared  = "shared"

	publicationKind = "skill-publication"
	receiptKind     = "skill-publish-receipt"
)

type Version struct {
	Name     string `json:"name"`
	Revision int64  `json:"revision"`
	Digest   string `json:"digest"`
}

type Manifest struct {
	Builtin Version   `json:"builtin"`
	Shared  []Version `json:"shared"`
}

type Selector struct {
	Kind     string `json:"kind"`
	Name     string `json:"name,omitempty"`
	Revision int64  `json:"revision,omitempty"`
	Digest   string `json:"digest,omitempty"`
}

type PublishRequest struct {
	Name         string `json:"name"`
	BaseRevision int64  `json:"base_revision"`
	Digest       string `json:"digest"`
	DraftID      string `json:"draft_id"`
	ID           string `json:"id"`
}

type publication struct {
	domain.Meta
	Name   string `json:"name"`
	Digest string `json:"digest"`
}

func (p publication) version() Version {
	return Version{Name: p.Name, Revision: p.Revision, Digest: p.Digest}
}

type publishReceipt struct {
	domain.Meta
	Request   PublishRequest `json:"request"`
	Published Version        `json:"published"`
}

type Service struct {
	store          *persistence.Store
	snapshotDir    string
	builtin        Version
	builtinArchive []byte
	snapshotMu     sync.Mutex
}

//go:embed builtin
var builtinFiles embed.FS

var embeddedBundle = sync.OnceValues(func() (bundle struct {
	data   []byte
	digest string
}, err error) {
	source, err := fs.Sub(builtinFiles, "builtin")
	if err != nil {
		return bundle, err
	}
	var out bytes.Buffer
	if err = writeArchive(context.Background(), source, false, &out); err != nil {
		return bundle, err
	}
	bundle.data, bundle.digest = out.Bytes(), Digest(out.Bytes())
	return bundle, nil
})

func New(store *persistence.Store) (*Service, error) {
	bundle, err := embeddedBundle()
	if err != nil {
		return nil, fmt.Errorf("build embedded skills: %w", err)
	}
	root := filepath.Join(store.Directory, "skills")
	snapshotDir := filepath.Join(root, "snapshots")
	if err = os.MkdirAll(snapshotDir, 0700); err != nil {
		return nil, err
	}
	for _, dir := range []string{snapshotDir, root, store.Directory} {
		if err = syncDirectory(dir); err != nil {
			return nil, err
		}
	}
	return &Service{
		store: store, snapshotDir: snapshotDir,
		builtin: Version{Digest: bundle.digest}, builtinArchive: bundle.data,
	}, nil
}

func (s *Service) Manifest(ctx context.Context) (Manifest, error) {
	rows, err := s.store.List(ctx, publicationKind)
	if err != nil {
		return Manifest{}, err
	}
	manifest := Manifest{Builtin: s.builtin, Shared: make([]Version, 0, len(rows))}
	for _, row := range rows {
		var record publication
		if err = json.Unmarshal(row.Data, &record); err != nil {
			return Manifest{}, err
		}
		manifest.Shared = append(manifest.Shared, record.version())
	}
	return manifest, nil
}

func (s *Service) Download(ctx context.Context, sel Selector) (Version, io.ReadCloser, error) {
	if err := ctx.Err(); err != nil {
		return Version{}, nil, err
	}
	switch sel.Kind {
	case Builtin:
		if sel.Name != "" || sel.Revision != 0 || !validDigest(sel.Digest) {
			return Version{}, nil, domain.Invalid("Built-in download requires its bundle digest")
		}
		if sel.Digest != s.builtin.Digest {
			return Version{}, nil, domain.Missing("Built-in bundle", sel.Digest)
		}
		return s.builtin, io.NopCloser(bytes.NewReader(s.builtinArchive)), nil
	case Shared:
		if !ValidName(sel.Name) || sel.Revision <= 0 || sel.Digest != "" && !validDigest(sel.Digest) {
			return Version{}, nil, domain.Invalid("Shared download requires a skill name and positive revision")
		}
		var record publication
		if err := s.store.Version(ctx, publicationKind, sel.Name, sel.Revision, &record); err != nil {
			return Version{}, nil, err
		}
		if !validDigest(record.Digest) {
			return Version{}, nil, errors.New("published skill record has an invalid digest")
		}
		if sel.Digest != "" && sel.Digest != record.Digest {
			return Version{}, nil, domain.Missing("Shared snapshot", sel.Digest)
		}
		file, err := os.Open(s.snapshotPath(record.Digest))
		if err != nil {
			return Version{}, nil, err
		}
		info, err := file.Stat()
		if err != nil || !info.Mode().IsRegular() || info.Size() > MaxArchiveBytes {
			file.Close()
			if err == nil {
				err = errors.New("published skill snapshot has an invalid size or type")
			}
			return Version{}, nil, err
		}
		return record.version(), file, nil
	default:
		return Version{}, nil, domain.Invalid("skill kind must be builtin or shared")
	}
}

func checkPublishAccess(ctx context.Context, tx *persistence.Tx, b domain.ExecutionBinding) error {
	var agent domain.Agent
	if err := tx.Get(ctx, "agents", b.AgentID, &agent); err != nil {
		return err
	}
	if agent.Deletion || !slices.Contains(agent.AllowedComputers, b.ComputerID) {
		return domain.Forbidden("the Bot may not use this Computer")
	}
	var computer domain.Computer
	if err := tx.Get(ctx, "computers", b.ComputerID, &computer); err != nil {
		return err
	}
	if computer.Revoked {
		return domain.Forbidden("Computer access was revoked")
	}
	if computer.ComputerAPIDisabled {
		return domain.Forbidden("Astral API is disabled on the source Computer")
	}
	var session domain.Session
	if err := tx.Get(ctx, "sessions", b.SessionID, &session); err != nil {
		return err
	}
	if session.AgentID != b.AgentID || session.Deletion != nil {
		return domain.Forbidden("the publishing Session is unavailable")
	}
	return nil
}

var publishID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

func validatePublishRequest(req PublishRequest) error {
	if !ValidName(req.Name) || req.BaseRevision < 0 || !validDigest(req.Digest) ||
		!publishID.MatchString(req.ID) || !publishID.MatchString(req.DraftID) {
		return domain.Invalid("publish requires a valid skill name, base revision, digest, draft ID and request ID")
	}
	return nil
}

func getReceipt(ctx context.Context, tx *persistence.Tx, b domain.ExecutionBinding, req PublishRequest) (Version, bool, error) {
	var receipt publishReceipt
	err := tx.Get(ctx, receiptKind, receiptID(b, req.ID), &receipt)
	if domain.IsMissing(err) {
		return Version{}, false, nil
	}
	if err != nil {
		return Version{}, false, err
	}
	if receipt.Request != req {
		return Version{}, false, domain.Conflict("the publish request ID was used for different draft content")
	}
	return receipt.Published, true, nil
}

func receiptID(b domain.ExecutionBinding, requestID string) string {
	return Digest([]byte(b.AgentID + "\x00" + b.ComputerID + "\x00" + requestID))
}

func (s *Service) Receipt(ctx context.Context, b domain.ExecutionBinding, req PublishRequest) (Version, bool, error) {
	if err := validatePublishRequest(req); err != nil {
		return Version{}, false, err
	}
	var version Version
	var found bool
	err := s.store.Update(ctx, func(tx *persistence.Tx) error {
		if err := checkPublishAccess(ctx, tx, b); err != nil {
			return err
		}
		var err error
		version, found, err = getReceipt(ctx, tx, b, req)
		return err
	})
	return version, found, err
}

type reader interface {
	Get(context.Context, string, string, any) error
}

func checkBase(ctx context.Context, db reader, req PublishRequest) (publication, error) {
	record := publication{Meta: domain.Meta{ID: req.Name}, Name: req.Name}
	err := db.Get(ctx, publicationKind, req.Name, &record)
	if err != nil && !domain.IsMissing(err) {
		return publication{}, err
	}
	if record.Revision != req.BaseRevision {
		return publication{}, domain.Conflict(fmt.Sprintf("Shared Skill %q has revision %d, not base revision %d", req.Name, record.Revision, req.BaseRevision))
	}
	return record, nil
}

func (s *Service) Publish(ctx context.Context, b domain.ExecutionBinding, req PublishRequest, data []byte) (Version, error) {
	version, found, err := s.Receipt(ctx, b, req)
	if err != nil || found {
		return version, err
	}
	if _, err = checkBase(ctx, s.store, req); err != nil {
		// A concurrent retry can commit after the first receipt lookup.
		if v, done, lookupErr := s.Receipt(ctx, b, req); done || lookupErr != nil {
			return v, lookupErr
		}
		return Version{}, err
	}
	if len(data) > MaxArchiveBytes || Digest(data) != req.Digest {
		return Version{}, domain.Invalid("published snapshot does not match its digest or exceeds 16 MiB")
	}
	if err = validateArchive(ctx, data, req.Name); err != nil {
		if ctx.Err() != nil {
			return Version{}, ctx.Err()
		}
		return Version{}, domain.Invalid(err.Error())
	}
	if err = s.saveSnapshot(ctx, req.Digest, data); err != nil {
		return Version{}, err
	}
	err = s.store.Update(ctx, func(tx *persistence.Tx) error {
		if err := checkPublishAccess(ctx, tx, b); err != nil {
			return err
		}
		var err error
		version, found, err = getReceipt(ctx, tx, b, req)
		if err != nil || found {
			return err
		}
		record, err := checkBase(ctx, tx, req)
		if err != nil {
			return err
		}
		record.Digest = req.Digest
		if err = tx.Save(ctx, publicationKind, &record.Meta, &record); err != nil {
			return err
		}
		version = record.version()
		receipt := publishReceipt{Meta: domain.Meta{ID: receiptID(b, req.ID)}, Request: req, Published: version}
		return tx.Save(ctx, receiptKind, &receipt.Meta, &receipt)
	})
	if err != nil {
		return Version{}, err
	}
	return version, nil
}

func (s *Service) snapshotPath(digest string) string {
	return filepath.Join(s.snapshotDir, digest+".tar")
}

func (s *Service) saveSnapshot(ctx context.Context, digest string, data []byte) error {
	s.snapshotMu.Lock()
	defer s.snapshotMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	path := s.snapshotPath(digest)
	if existing, err := os.Open(path); err == nil {
		match := &archiveMatch{remaining: data}
		_, readErr := io.Copy(match, io.LimitReader(existing, MaxArchiveBytes+1))
		if err = existing.Close(); err != nil {
			return err
		}
		if readErr != nil || len(match.remaining) != 0 {
			return errors.Join(errors.New("stored Shared Skill snapshot does not match its digest"), readErr)
		}
		return syncDirectory(s.snapshotDir)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	file, err := os.CreateTemp(s.snapshotDir, ".incoming-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	_, err = file.Write(data)
	if err == nil {
		err = file.Sync()
	}
	if err = errors.Join(err, file.Close()); err != nil {
		return err
	}
	if err = commitSnapshot(file.Name(), path); err != nil {
		return err
	}
	// Make the immutable snapshot durable before the SQLite transaction
	// publishes a reference to it. An interruption can only leave an orphan.
	return syncDirectory(s.snapshotDir)
}
