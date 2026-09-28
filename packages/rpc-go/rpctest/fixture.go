// Package rpctest provides temporary PKI and real TLS fixtures for integration
// tests. No private key fixtures are stored in the repository.
package rpctest

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"easygo-agent/rpc"
)

type PKI struct {
	t      testing.TB
	dir    string
	ca     *x509.Certificate
	key    *ecdsa.PrivateKey
	CAFile string
}

func NewPKI(t testing.TB) *PKI {
	t.Helper()
	p := &PKI{t: t, dir: t.TempDir()}
	p.key = key(t)
	p.ca = &x509.Certificate{SerialNumber: serial(t), Subject: pkix.Name{CommonName: "test CA"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	raw, e := x509.CreateCertificate(rand.Reader, p.ca, p.ca, &p.key.PublicKey, p.key)
	if e != nil {
		t.Fatal(e)
	}
	p.CAFile = filepath.Join(p.dir, "ca.crt")
	write(t, p.CAFile, "CERTIFICATE", raw)
	return p
}
func key(t testing.TB) *ecdsa.PrivateKey {
	k, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	return k
}
func serial(t testing.TB) *big.Int {
	n, e := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 120))
	if e != nil {
		t.Fatal(e)
	}
	return n
}
func write(t testing.TB, path, kind string, raw []byte) {
	if e := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: kind, Bytes: raw}), 0600); e != nil {
		t.Fatal(e)
	}
}
func (p *PKI) Issue(name string, expired bool) rpc.TLSConfig {
	p.t.Helper()
	k := key(p.t)
	cert := &x509.Certificate{SerialNumber: serial(p.t), Subject: pkix.Name{CommonName: "same-untrusted-subject"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth}, DNSNames: []string{"localhost"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}}
	if expired {
		cert.NotBefore = time.Now().Add(-2 * time.Hour)
		cert.NotAfter = time.Now().Add(-time.Hour)
	}
	raw, e := x509.CreateCertificate(rand.Reader, cert, p.ca, &k.PublicKey, p.key)
	if e != nil {
		p.t.Fatal(e)
	}
	c := rpc.TLSConfig{CertFile: filepath.Join(p.dir, name+".crt"), KeyFile: filepath.Join(p.dir, name+".key"), CAFile: p.CAFile}
	write(p.t, c.CertFile, "CERTIFICATE", raw)
	priv, e := x509.MarshalPKCS8PrivateKey(k)
	if e != nil {
		p.t.Fatal(e)
	}
	write(p.t, c.KeyFile, "PRIVATE KEY", priv)
	return c
}
func Start(t testing.TB, s *http.Server) *httptest.Server {
	t.Helper()
	ts := httptest.NewUnstartedServer(s.Handler)
	ts.TLS = s.TLSConfig
	ts.Config.ErrorLog = nil
	ts.StartTLS()
	t.Cleanup(ts.Close)
	return ts
}
func Client(t testing.TB, c rpc.TLSConfig, peer string) *http.Client {
	t.Helper()
	cfg, e := rpc.ClientTLS(c, peer)
	if e != nil {
		t.Fatal(e)
	}
	return ClientConfig(t, cfg)
}
func ClientConfig(t testing.TB, c *tls.Config) *http.Client {
	transport := &http.Transport{TLSClientConfig: c}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}
func Call(t testing.TB, c *http.Client, url, method, params string) (int, rpc.Envelope) {
	t.Helper()
	body := `{"jsonrpc":"2.0","id":"test-id","method":` + quote(method) + `,"params":` + params + `}`
	return Raw(t, c, url, body)
}
func quote(v string) string { b, _ := json.Marshal(v); return string(b) }
func Raw(t testing.TB, c *http.Client, url, body string) (int, rpc.Envelope) {
	t.Helper()
	resp, e := c.Post(url+"/rpc", "application/json", strings.NewReader(body))
	if e != nil {
		t.Fatal(e)
	}
	defer resp.Body.Close()
	raw, e := io.ReadAll(resp.Body)
	if e != nil {
		t.Fatal(e)
	}
	var out rpc.Envelope
	if e = json.Unmarshal(raw, &out); e != nil {
		t.Fatalf("invalid JSON reply: %s: %v", raw, e)
	}
	return resp.StatusCode, out
}
