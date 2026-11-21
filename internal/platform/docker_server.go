//go:build !daemon

package platform

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/oines/astral/internal/computers"
	"github.com/oines/astral/internal/domain"
)

// Older local containers have no reported metadata, but their creation records
// retain the exact naming and volume identity. Recover only those known values;
// host/image details must come from the creator, not from the current Server.
func (s *Server) restoreContainerMetadata(ctx context.Context) error {
	records, err := s.Store.List(ctx, "provisioning")
	if err != nil {
		return err
	}
	for _, raw := range records {
		var operation domain.Provisioning
		if err := json.Unmarshal(raw.Data, &operation); err != nil {
			return err
		}
		if operation.Kind != "container" || operation.ComputerID == "" {
			continue
		}
		var computer domain.Computer
		if err := s.Store.Get(ctx, "computers", operation.ComputerID, &computer); err != nil {
			if domain.IsMissing(err) {
				continue
			}
			return err
		}
		if computer.Kind != "container" || computer.Container != nil {
			continue
		}
		name := "astral-" + operation.ID
		computer.Container = &domain.ContainerInfo{Name: name, Volume: name + "-data", MountPath: "/var/lib/astral"}
		if computer.Root == "" {
			computer.Root = "/var/lib/astral"
		}
		if err := s.Store.Save(ctx, "computers", &computer.Meta, &computer); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) CreateContainer(ctx context.Context, intent string, computerAPIDisabled *bool) (domain.Provisioning, error) {
	if len(intent) > 200 {
		return domain.Provisioning{}, domain.Invalid("provisioning request ID is too long")
	}
	sum := sha256.Sum256([]byte(s.Config.DataDirectory + "\x00" + intent))
	id := "container_" + hex.EncodeToString(sum[:12])
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return domain.Provisioning{}, domain.Offline("Server is shutting down")
	}
	var operation domain.Provisioning
	err := s.Store.Get(ctx, "provisioning", id, &operation)
	if err != nil && !domain.IsMissing(err) {
		return operation, err
	}
	if err == nil && computerAPIDisabled != nil && operation.ComputerAPIDisabled != *computerAPIDisabled {
		return operation, domain.Conflict("the original container request uses a different Astral API setting")
	}
	if err == nil && (operation.State == "connected" || s.provisioning[id]) {
		return operation, nil
	}
	if domain.IsMissing(err) {
		operation = domain.Provisioning{Meta: domain.Meta{ID: id}, IntentID: intent, Kind: "container", State: "preparing", Phase: "checking_runtime", Log: []string{}}
		operation.ComputerAPIDisabled = computerAPIDisabled != nil && *computerAPIDisabled
	}
	operation.State, operation.Error = "preparing", ""
	if err = s.Store.Save(ctx, "provisioning", &operation.Meta, &operation); err != nil {
		return operation, err
	}
	s.provisioning[id] = true
	s.workers.Add(1)
	go func(value domain.Provisioning) {
		defer s.workers.Done()
		defer func() { s.mu.Lock(); delete(s.provisioning, id); s.mu.Unlock() }()
		s.provision(value)
	}(operation)
	return operation, nil
}

func (s *Server) provision(operation domain.Provisioning) {
	// The complete workspace image can take time on a first install or a slow
	// network. Leave ten minutes inside the fixed 90-minute enrollment window
	// for the actual Daemon registration and online confirmation.
	ctx, cancel := context.WithTimeout(s.ctx, 80*time.Minute)
	defer cancel()
	save := func() {
		persist, done := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer done()
		_ = s.Store.Save(persist, "provisioning", &operation.Meta, &operation)
	}
	fail := func(err error) {
		operation.State, operation.Phase, operation.Error = "failed", "failed", err.Error()
		save()
	}
	report := func(phase, line string) {
		operation.Phase = phase
		operation.State = "creating"
		operation.Log = append(operation.Log, line)
		if len(operation.Log) > 80 {
			operation.Log = operation.Log[len(operation.Log)-80:]
		}
		save()
	}
	probe, err := ProbeDocker(ctx)
	if err != nil {
		fail(err)
		return
	}
	ctx = dockerContext(ctx, probe)
	root := filepath.Join(s.Config.ComputerDirectory, "provisioning", operation.ID)
	var installation ContainerInstallation
	if readErr := ReadJSON(filepath.Join(root, "container.json"), &installation); readErr == nil {
		if installation.Identity != operation.ID {
			fail(domain.Conflict("this directory belongs to another container operation"))
			return
		}
		if installation.Endpoint != "" && installation.Endpoint != probe.Endpoint {
			fail(domain.Conflict("this container belongs to another local Docker endpoint; select its original context"))
			return
		}
	} else if !errors.Is(readErr, os.ErrNotExist) {
		fail(readErr)
		return
	}
	binary, err := s.binary("linux", probe.Arch)
	if err != nil {
		fail(err)
		return
	}
	enrollmentPath := filepath.Join(root, "enrollment.json")
	var enrollment ConnectOptions
	alreadyRegistered := false
	refreshEnrollment := false
	if operation.EnrollmentID != "" {
		if err = ReadJSON(enrollmentPath, &enrollment); err != nil {
			fail(err)
			return
		}
		// Registered identities remain in their data volume. An expired unused
		// pairing is replaced; retry must never resurrect a previously leaked token.
		var record domain.Enrollment
		if err = s.Store.Get(ctx, "enrollments", operation.EnrollmentID, &record); err != nil {
			fail(err)
			return
		}
		alreadyRegistered = record.State == "registered"
		if !alreadyRegistered && !isLocalContainerAddress(enrollment.ServerURL, s.URL) {
			existing, findErr := findExistingContainer(ctx, operation.ID)
			if findErr != nil {
				fail(findErr)
				return
			}
			if existing != "" {
				fail(domain.Conflict("this existing container uses a non-local Server address; update its connection explicitly before retrying"))
				return
			}
		}
		if !alreadyRegistered && time.Now().After(record.ExpiresAt) {
			operation.EnrollmentID = ""
			refreshEnrollment = true
		}
	}
	if operation.EnrollmentID == "" {
		localURL := s.URL
		if refreshEnrollment && isLocalContainerAddress(enrollment.ServerURL, s.URL) {
			// The original container retains its network. Keep its previously
			// selected reachable route when replacing only the pairing token.
			localURL = enrollment.ServerURL
		}
		result, enrollErr := s.Hub.Enroll(ctx, localURL, computers.EnrollmentOptions{Container: true, ComputerAPIDisabled: operation.ComputerAPIDisabled})
		if enrollErr != nil {
			fail(enrollErr)
			return
		}
		operation.EnrollmentID = result.Enrollment.ID
		enrollment = ConnectOptions{ServerURL: localURL, ServerPin: result.ServerPin, EnrollmentID: result.Enrollment.ID, Token: result.Token}
		if err = WriteJSON(enrollmentPath, enrollment); err != nil {
			fail(err)
			return
		}
		save()
	}
	if !alreadyRegistered {
		if err = s.Hub.PrepareContainer(ctx, enrollment.EnrollmentID, enrollment.Token); err != nil {
			fail(err)
			return
		}
	}
	id, err := LaunchContainer(ctx, DockerOptions{Identity: operation.ID, Binary: binary, EnrollmentFile: enrollmentPath, Runtime: &probe, LocalServerURL: s.URL, RefreshEnrollment: refreshEnrollment}, report)
	operation.ContainerID = id
	if err != nil {
		fail(err)
		return
	}
	operation.State, operation.Phase = "connecting", "waiting_for_daemon"
	save()
	connectCtx, done := context.WithTimeout(ctx, 45*time.Second)
	defer done()
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	for {
		var record domain.Enrollment
		if err = s.Store.Get(connectCtx, "enrollments", operation.EnrollmentID, &record); err == nil && record.ComputerID != "" {
			operation.ComputerID = record.ComputerID
			status, waitErr := s.waitComputer(connectCtx, record.ComputerID)
			if waitErr == nil && status.Online {
				operation.State, operation.Phase = "connected", "connected"
				save()
				return
			}
		}
		select {
		case <-connectCtx.Done():
			logs, _ := dockerOutput(ctx, "logs", "--tail", "20", id)
			scanner := bufio.NewScanner(strings.NewReader(string(logs)))
			for scanner.Scan() {
				report("waiting_for_daemon", scanner.Text())
			}
			fail(domain.Offline("Container started but its Daemon did not connect. Check its log, Server URL, and local container networking or firewall."))
			return
		case <-tick.C:
		}
	}
}
