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
	Removed []Version `json:"removed,omitempty"`
}

type Selector struct {
	Kind     string `json:"kind"`
	Name     string `json:"name,omitempty"`
	Revision int64  `json:"revision,omitempty"`
	Digest   string `json:"digest,omitempty"`
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
	Published Version `json:"published"`
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
		if record.Digest == "" {
			manifest.Removed = append(manifest.Removed, record.version())
		} else {
			manifest.Shared = append(manifest.Shared, record.version())
		}
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
		if err := s.store.Get(ctx, publicationKind, sel.Name, &record); err != nil {
			return Version{}, nil, err
		}
		if record.Digest == "" || record.Revision != sel.Revision || sel.Digest != "" && sel.Digest != record.Digest {
			return Version{}, nil, domain.Missing("current Shared Skill revision", sel.Name)
		}
		if !validDigest(record.Digest) {
			return Version{}, nil, errors.New("published skill record has an invalid digest")
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
		return domain.Forbidden("the requesting Session is unavailable")
	}
	return nil
}

var requestID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,128}$`)

func validateRequest(name, id string) error {
	if !ValidName(name) || !requestID.MatchString(id) {
		return domain.Invalid("shared skill operations require a valid name and Idempotency-Key")
	}
	return nil
}

func getReceipt(ctx context.Context, tx *persistence.Tx, b domain.ExecutionBinding, name, id string, deleting bool) (Version, bool, error) {
	var receipt publishReceipt
	err := tx.Get(ctx, receiptKind, receiptID(b, id), &receipt)
	if domain.IsMissing(err) {
		return Version{}, false, nil
	}
	if err != nil {
		return Version{}, false, err
	}
	if receipt.Published.Name != name || (receipt.Published.Digest == "") != deleting {
		return Version{}, false, domain.Conflict("the request ID was used for a different shared skill operation")
	}
	return receipt.Published, true, nil
}

func receiptID(b domain.ExecutionBinding, requestID string) string {
	return Digest([]byte(b.AgentID + "\x00" + b.ComputerID + "\x00" + requestID))
}

func (s *Service) publishReceipt(ctx context.Context, b domain.ExecutionBinding, name, id string) (Version, bool, error) {
	var version Version
	var found bool
	err := s.store.Update(ctx, func(tx *persistence.Tx) error {
		if err := checkPublishAccess(ctx, tx, b); err != nil {
			return err
		}
		var err error
		version, found, err = getReceipt(ctx, tx, b, name, id, false)
		return err
	})
	return version, found, err
}

type reader interface {
	Get(context.Context, string, string, any) error
}

func checkBase(ctx context.Context, db reader, name string, base int64) (publication, error) {
	record := publication{Meta: domain.Meta{ID: name}, Name: name}
	err := db.Get(ctx, publicationKind, name, &record)
	if err != nil && !domain.IsMissing(err) {
		return publication{}, err
	}
	if record.Revision != base {
		return publication{}, domain.Conflict(fmt.Sprintf("Shared Skill %q changed on the Server. Sync this Computer before publishing again.", name))
	}
	return record, nil
}

func (s *Service) Publish(ctx context.Context, b domain.ExecutionBinding, name, id string, export func() ([]byte, int64, error)) (Version, error) {
	if err := validateRequest(name, id); err != nil {
		return Version{}, err
	}
	version, found, err := s.publishReceipt(ctx, b, name, id)
	if err != nil || found {
		return version, err
	}
	data, base, err := export()
	if err != nil {
		return Version{}, err
	}
	if _, err = checkBase(ctx, s.store, name, base); err != nil {
		// A concurrent retry can commit after the first receipt lookup.
		if v, done, lookupErr := s.publishReceipt(ctx, b, name, id); done || lookupErr != nil {
			return v, lookupErr
		}
		return Version{}, err
	}
	if len(data) > MaxArchiveBytes {
		return Version{}, domain.Invalid("published snapshot exceeds 16 MiB")
	}
	if err = validateArchive(ctx, data, name); err != nil {
		if ctx.Err() != nil {
			return Version{}, ctx.Err()
		}
		return Version{}, domain.Invalid(err.Error())
	}
	digest := Digest(data)
	if err = s.saveSnapshot(ctx, digest, data); err != nil {
		return Version{}, err
	}
	err = s.store.Update(ctx, func(tx *persistence.Tx) error {
		if err := checkPublishAccess(ctx, tx, b); err != nil {
			return err
		}
		var err error
		version, found, err = getReceipt(ctx, tx, b, name, id, false)
		if err != nil || found {
			return err
		}
		record, err := checkBase(ctx, tx, name, base)
		if err != nil {
			return err
		}
		record.Digest = digest
		if err = tx.Save(ctx, publicationKind, &record.Meta, &record); err != nil {
			return err
		}
		version = record.version()
		return saveReceipt(ctx, tx, b, id, version)
	})
	if err != nil {
		return Version{}, err
	}
	return version, nil
}

func (s *Service) Delete(ctx context.Context, b domain.ExecutionBinding, name, id string) (Version, error) {
	if err := validateRequest(name, id); err != nil {
		return Version{}, err
	}
	var version Version
	err := s.store.Update(ctx, func(tx *persistence.Tx) error {
		if err := checkPublishAccess(ctx, tx, b); err != nil {
			return err
		}
		previous, found, err := getReceipt(ctx, tx, b, name, id, true)
		if err != nil || found {
			version = previous
			return err
		}
		var record publication
		if err := tx.Get(ctx, publicationKind, name, &record); err != nil {
			if domain.IsMissing(err) {
				return domain.Missing("Shared Skill", name)
			}
			return err
		}
		if record.Digest != "" {
			// Keep the revision monotonic when a deleted name is published again.
			record.Digest = ""
			if err := tx.Save(ctx, publicationKind, &record.Meta, &record); err != nil {
				return err
			}
		}
		version = record.version()
		return saveReceipt(ctx, tx, b, id, version)
	})
	if err != nil {
		return Version{}, err
	}
	return version, nil
}

func saveReceipt(ctx context.Context, tx *persistence.Tx, b domain.ExecutionBinding, id string, version Version) error {
	receipt := publishReceipt{Meta: domain.Meta{ID: receiptID(b, id)}, Published: version}
	return tx.Save(ctx, receiptKind, &receipt.Meta, &receipt)
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
