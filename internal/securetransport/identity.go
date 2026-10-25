// Package securetransport provides the pinned Server identity used by the
// encrypted Computer tunnel. The outer HTTP/WebSocket carries no credentials.
package securetransport

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

type Identity struct {
	certificate tls.Certificate
	pin         string
}

// Load creates the identity once. A damaged existing identity is an error;
// it is never replaced silently, since that would break established trust.
func Load(directory string) (*Identity, error) {
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	if err := protectPrivate(directory); err != nil {
		return nil, err
	}
	path := filepath.Join(directory, "identity.pem")
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		public, private, generateErr := ed25519.GenerateKey(rand.Reader)
		if generateErr != nil {
			return nil, generateErr
		}
		serial, generateErr := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
		if generateErr != nil {
			return nil, generateErr
		}
		now := time.Now().UTC()
		template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "Astral Server"}, NotBefore: now.Add(-24 * time.Hour), NotAfter: now.AddDate(100, 0, 0), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true}
		certificate, generateErr := x509.CreateCertificate(rand.Reader, template, template, public, private)
		if generateErr != nil {
			return nil, generateErr
		}
		key, generateErr := x509.MarshalPKCS8PrivateKey(private)
		if generateErr != nil {
			return nil, generateErr
		}
		raw = append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificate}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key})...)
		file, createErr := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if errors.Is(createErr, os.ErrExist) {
			raw, err = os.ReadFile(path)
		} else if createErr != nil {
			return nil, createErr
		} else {
			if protectErr := protectPrivate(path); protectErr != nil {
				_ = file.Close()
				_ = os.Remove(path)
				return nil, protectErr
			}
			_, writeErr := file.Write(raw)
			err = errors.Join(writeErr, file.Sync(), file.Close())
		}
	}
	if err != nil {
		return nil, err
	}
	if err = protectPrivate(path); err != nil {
		return nil, err
	}
	certificate, err := tls.X509KeyPair(raw, raw)
	if err != nil {
		return nil, fmt.Errorf("invalid Server tunnel identity: %w", err)
	}
	leaf, err := x509.ParseCertificate(certificate.Certificate[0])
	if err != nil {
		return nil, err
	}
	certificate.Leaf = leaf
	hash := sha256.Sum256(leaf.RawSubjectPublicKeyInfo)
	return &Identity{certificate: certificate, pin: hex.EncodeToString(hash[:])}, nil
}

func (i *Identity) Pin() string { return i.pin }
func (i *Identity) ServerConfig() *tls.Config {
	config, _ := i.ServerConfigForProtocol("astral-computer/1")
	return config
}

// ServerConfigForProtocol shares the persistent pinned identity across the
// Computer tunnel and the separate machine-local HTTPS administrative channel.
func (i *Identity) ServerConfigForProtocol(protocol string) (*tls.Config, error) {
	if protocol != "astral-computer/1" && protocol != "http/1.1" {
		return nil, errors.New("unsupported pinned TLS protocol")
	}
	return &tls.Config{MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13, Certificates: []tls.Certificate{i.certificate}, NextProtos: []string{protocol}}, nil
}

func ClientConfig(pin string) (*tls.Config, error) {
	return ClientConfigForProtocol(pin, "astral-computer/1")
}

// ClientConfigForProtocol verifies the exact SPKI pin and the fixed negotiated
// ALPN before the caller can send authentication or application data.
func ClientConfigForProtocol(pin, protocol string) (*tls.Config, error) {
	if protocol != "astral-computer/1" && protocol != "http/1.1" {
		return nil, errors.New("unsupported pinned TLS protocol")
	}
	expected, err := hex.DecodeString(pin)
	if err != nil || len(expected) != sha256.Size {
		return nil, errors.New("a valid Server public-key pin is required; copy a new pairing command")
	}
	return &tls.Config{MinVersion: tls.VersionTLS13, MaxVersion: tls.VersionTLS13, NextProtos: []string{protocol},
		// This private identity uses explicit SPKI pinning, rather than public
		// PKI or a DNS name. Outer HTTPS retains ordinary CA verification.
		InsecureSkipVerify: true,
		VerifyConnection: func(state tls.ConnectionState) error {
			if len(state.PeerCertificates) != 1 || state.NegotiatedProtocol != protocol {
				return errors.New("invalid Server tunnel identity")
			}
			certificate := state.PeerCertificates[0]
			hash := sha256.Sum256(certificate.RawSubjectPublicKeyInfo)
			if subtle.ConstantTimeCompare(hash[:], expected) != 1 {
				return errors.New("Server identity does not match the paired public key")
			}
			if err := certificate.CheckSignature(certificate.SignatureAlgorithm, certificate.RawTBSCertificate, certificate.Signature); err != nil {
				return errors.New("invalid Server identity certificate signature")
			}
			now := time.Now()
			if now.Before(certificate.NotBefore) || now.After(certificate.NotAfter) {
				return errors.New("Server identity certificate is outside its validity period")
			}
			return nil
		}}, nil
}
