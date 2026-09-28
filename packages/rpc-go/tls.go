package rpc

import (
	"bytes"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
)

type TLSConfig struct {
	CertFile string `json:"cert_file"`
	KeyFile  string `json:"key_file"`
	CAFile   string `json:"ca_file"`
}
type Authorization struct {
	ID         string   `json:"id"`
	CertFile   string   `json:"cert_file"`
	Methods    []string `json:"methods"`
	Namespaces []string `json:"namespaces"`
}
type ServerConfig struct {
	// Audit is optional for library consumers and must be safe for concurrent calls.
	Audit         func(Audit)     `json:"-"`
	Listen        string          `json:"listen"`
	TLS           TLSConfig       `json:"tls"`
	Authorization []Authorization `json:"authorization"`
}
type permissions map[[32]byte]Authorization

func certificate(path string) (*x509.Certificate, error) {
	raw, e := os.ReadFile(path)
	if e != nil {
		return nil, errors.New("cannot read public certificate")
	}
	block, rest := pem.Decode(raw)
	if block == nil || block.Type != "CERTIFICATE" {
		return nil, errors.New("invalid public certificate")
	}
	// A pin names exactly one leaf, never an ambiguous certificate bundle.
	if len(bytes.TrimSpace(rest)) != 0 {
		return nil, errors.New("peer certificate must contain one certificate")
	}
	cert, e := x509.ParseCertificate(block.Bytes)
	if e != nil {
		return nil, errors.New("invalid public certificate")
	}
	return cert, nil
}
func identity(c TLSConfig) (*tls.Config, error) {
	cert, e := tls.LoadX509KeyPair(c.CertFile, c.KeyFile)
	if e != nil {
		return nil, errors.New("cannot load TLS identity")
	}
	raw, e := os.ReadFile(c.CAFile)
	if e != nil {
		return nil, errors.New("cannot read trust bundle")
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(raw) {
		return nil, errors.New("invalid trust bundle")
	}
	return &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert}, RootCAs: pool, ClientCAs: pool}, nil
}
func authorize(entries []Authorization) (permissions, error) {
	p := permissions{}
	for _, entry := range entries {
		if entry.ID == "" || len(entry.Methods) == 0 {
			return nil, errors.New("authorization requires id and methods")
		}
		for _, ns := range entry.Namespaces {
			if ns != "*" && !ValidNamespace(ns) {
				return nil, errors.New("invalid authorized namespace")
			}
		}
		for _, method := range entry.Methods {
			if method == "" {
				return nil, errors.New("invalid authorized method")
			}
		}
		cert, e := certificate(entry.CertFile)
		if e != nil {
			return nil, e
		}
		pin := sha256.Sum256(cert.Raw)
		if _, exists := p[pin]; exists {
			return nil, errors.New("duplicate authorized certificate")
		}
		p[pin] = entry
	}
	return p, nil
}
func serverTLS(c TLSConfig, p permissions) (*tls.Config, error) {
	cfg, e := identity(c)
	if e != nil {
		return nil, e
	}
	cfg.ClientAuth = tls.RequireAndVerifyClientCert
	cfg.VerifyConnection = func(s tls.ConnectionState) error {
		if len(s.VerifiedChains) == 0 || len(s.PeerCertificates) == 0 {
			return errors.New("unverified peer")
		}
		if _, ok := p[sha256.Sum256(s.PeerCertificates[0].Raw)]; !ok {
			return errors.New("unauthorized certificate")
		}
		return nil
	}
	return cfg, nil
}

// ClientTLS preserves Go's chain and hostname verification and adds a leaf pin.
// Configure each outbound transport separately for its intended peer.
func ClientTLS(c TLSConfig, peerCertificateFile string) (*tls.Config, error) {
	cfg, e := identity(c)
	if e != nil {
		return nil, e
	}
	cert, e := certificate(peerCertificateFile)
	if e != nil {
		return nil, e
	}
	pin := sha256.Sum256(cert.Raw)
	cfg.VerifyConnection = func(s tls.ConnectionState) error {
		if len(s.VerifiedChains) == 0 || len(s.PeerCertificates) == 0 || sha256.Sum256(s.PeerCertificates[0].Raw) != pin {
			return errors.New("server certificate mismatch")
		}
		return nil
	}
	return cfg, nil
}
