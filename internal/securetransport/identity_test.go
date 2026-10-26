package securetransport

import (
	"context"
	"crypto/tls"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPersistentIdentityAndPinnedTLS(t *testing.T) {
	directory := t.TempDir()
	identity, err := Load(directory)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(directory)
	if err != nil || loaded.Pin() != identity.Pin() {
		t.Fatalf("identity changed across restart: %v", err)
	}
	certificate := identity.certificate.Leaf
	if certificate.NotAfter.Before(time.Now().AddDate(99, 0, 0)) {
		t.Fatal("identity is not long lived")
	}
	for _, pin := range []string{identity.Pin(), strings.Repeat("0", 64)} {
		clientConfig, err := ClientConfig(pin)
		if err != nil {
			t.Fatal(err)
		}
		clientRaw, serverRaw := net.Pipe()
		client, server := tls.Client(clientRaw, clientConfig), tls.Server(serverRaw, identity.ServerConfig())
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		done := make(chan error, 1)
		go func() { done <- server.HandshakeContext(ctx) }()
		err = client.HandshakeContext(ctx)
		_ = clientRaw.Close()
		_ = serverRaw.Close()
		<-done
		cancel()
		if (err == nil) != (pin == identity.Pin()) {
			t.Fatalf("pin match=%v handshake=%v", pin == identity.Pin(), err)
		}
	}
	if err = os.WriteFile(filepath.Join(directory, "identity.pem"), []byte("damaged"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = Load(directory); err == nil {
		t.Fatal("silently replaced damaged identity")
	}
}

func TestInvalidPinsFailBeforeNetwork(t *testing.T) {
	for _, pin := range []string{"", "sha256:abc", strings.Repeat("z", 64), strings.Repeat("a", 63)} {
		if _, err := ClientConfig(pin); err == nil {
			t.Fatalf("accepted invalid pin %q", pin)
		}
	}
}

func TestPinnedTLSProtocolCannotBeDowngradedOrSwitched(t *testing.T) {
	identity, err := Load(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, protocol := range []string{"", "h2", "http/1.0", "arbitrary"} {
		if _, err := identity.ServerConfigForProtocol(protocol); err == nil {
			t.Fatalf("Server accepted protocol %q", protocol)
		}
		if _, err := ClientConfigForProtocol(identity.Pin(), protocol); err == nil {
			t.Fatalf("client accepted protocol %q", protocol)
		}
	}
	for _, serverProtocol := range []string{"http/1.1", "astral-computer/1"} {
		config, err := identity.ServerConfigForProtocol(serverProtocol)
		if err != nil {
			t.Fatal(err)
		}
		clientConfig, err := ClientConfigForProtocol(identity.Pin(), "http/1.1")
		if err != nil {
			t.Fatal(err)
		}
		clientRaw, serverRaw := net.Pipe()
		client, server := tls.Client(clientRaw, clientConfig), tls.Server(serverRaw, config)
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		done := make(chan error, 1)
		go func() { done <- server.HandshakeContext(ctx) }()
		err = client.HandshakeContext(ctx)
		_ = clientRaw.Close()
		_ = serverRaw.Close()
		<-done
		cancel()
		if (err == nil) != (serverProtocol == "http/1.1") {
			t.Fatalf("expected fixed HTTP ALPN, Server protocol %q handshake %v", serverProtocol, err)
		}
	}
}
