package platform

import (
	"context"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"github.com/oines/astral/internal/computers"
	"github.com/oines/astral/internal/daemonservice"
	"github.com/oines/astral/internal/domain"
	"github.com/oines/astral/internal/githubrelease"
	"github.com/oines/astral/internal/releasefeed"
)

func TestUpdateWaitsForNewBootAndExpectedVersion(t *testing.T) {
	var phase atomic.Int64
	_, credentials := secureStatusFixture(t, func() domain.ComputerStatus {
		status := domain.ComputerStatus{Online: true, BootID: "old"}
		status.DaemonVersion = "release"
		switch phase.Add(1) {
		case 1:
		case 2:
			status.BootID = "new"
			status.DaemonVersion = "wrong"
		default:
			status.BootID = "new"
		}
		return status
	})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := waitUpdatedComputer(ctx, credentials, "old", "release"); err != nil {
		t.Fatal(err)
	}
	if phase.Load() < 3 {
		t.Fatal("reported update before the new version connected")
	}
}

func TestSupervisedDaemonReportsResidentUpdateFormat(t *testing.T) {
	t.Setenv("ASTRAL_CONTAINER_SUPERVISOR", "1")
	for _, tc := range []struct {
		supervisorFormat string
		standalone       bool
	}{{"", false}, {"1", true}} {
		t.Setenv(standaloneSupervisorEnv, tc.supervisorFormat)
		d := &computers.Daemon{Hello: computers.Hello{Container: &domain.ContainerInfo{Name: "test"}}}
		configureDaemonUpdate(d)
		if !slices.Contains(d.Hello.Capabilities, computers.DaemonUpdateCapability) ||
			slices.Contains(d.Hello.Capabilities, computers.StandaloneUpdateCapability) != tc.standalone {
			t.Fatalf("resident supervisor format %q advertises %v", tc.supervisorFormat, d.Hello.Capabilities)
		}
	}
}

func TestUpdateServiceRetainsOrReplacesDownloadSource(t *testing.T) {
	const saved = "https://old.example/"
	const selected = "https://selected.example/"
	const ambient = "https://environment.example/"
	t.Setenv(githubrelease.ProxyEnvironment, ambient)
	previous := daemonservice.Spec{Executable: "old", GitHubProxy: new(saved)}
	target := &releasefeed.Binary{Version: "1.1.0", GitHubProxy: new(selected)}
	for _, tc := range []struct {
		name    string
		options UpdateOptions
		target  *releasefeed.Binary
		want    string
	}{
		{"explicit direct", UpdateOptions{ManagedHelper: true, DownloadSource: githubrelease.ProxyOverride{Set: true}}, target, ""},
		{"old Daemon new helper", UpdateOptions{Version: target.Version, ManagedHelper: true}, target, selected},
		{"older Server", UpdateOptions{ManagedHelper: true}, nil, saved},
		{"manual update environment", UpdateOptions{}, nil, ambient},
	} {
		t.Run(tc.name, func(t *testing.T) {
			next, err := tc.options.serviceSpec(previous, "updated", tc.target)
			if err != nil {
				t.Fatal(err)
			}
			if next.Executable != "updated" || next.GitHubProxy == nil || *next.GitHubProxy != tc.want || *previous.GitHubProxy != saved {
				t.Fatalf("update failed to preserve the selected download source and rollback spec: %+v", next)
			}
		})
	}
}
