// Package meter reserves credits before provider dispatch and durably retries
// usage receipts. A lost/incomplete provider response is never priced as free.
package meter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"easygo-agent/rpc"
	"easygo-agent/services/ai-gateway/ai"
	"easygo-agent/services/ai-gateway/gateway"
	bolt "go.etcd.io/bbolt"
)

type Config struct {
	URL                 string `json:"url"`
	PeerCertificateFile string `json:"peer_certificate_file"`
	Database            string `json:"database"`
	MaxOutputTokens     int    `json:"max_output_tokens,omitempty"`
	// RetentionSeconds keeps settled claims for duplicate detection (default
	// 604800, 7 days). AuthorizingTimeoutSeconds drops claims whose wallet
	// authorization never finished (default 3600). Zero keeps the default.
	RetentionSeconds          int64 `json:"retention_seconds,omitempty"`
	AuthorizingTimeoutSeconds int64 `json:"authorizing_timeout_seconds,omitempty"`
	// Observer, if set, receives a report after every background flush.
	Observer func(FlushReport) `json:"-"`
}

// FlushReport describes one background flush.
type FlushReport struct {
	Pending int    `json:"pending"`         // receipts the wallet has not acknowledged
	Expired int    `json:"expired"`         // claims removed by retention in this flush
	Error   string `json:"error,omitempty"` // the flush's last error, if any
}
type receipt struct {
	Namespace      string   `json:"namespace"`
	RequestID      string   `json:"request_id"`
	Usage          ai.Usage `json:"usage"`
	Outcome        string   `json:"outcome"`
	ProviderStatus int      `json:"provider_status,omitempty"`
	Source         string   `json:"source"`
}
type record struct {
	Reservation gateway.Reservation `json:"reservation"`
	State       string              `json:"state"`
	Receipt     *receipt            `json:"receipt,omitempty"`
	// Updated is when State last changed. New stamps claims written before
	// this field existed, so retention counts from the upgrade.
	Updated time.Time `json:"updated,omitzero"`
}
type Manager struct {
	db                 *bolt.DB
	client             *http.Client
	transport          *http.Transport
	url                string
	retention          time.Duration
	authorizingTimeout time.Duration
	observer           func(FlushReport)
	stopped            chan struct{}
	wg                 sync.WaitGroup
	once               sync.Once
	unhealthy          atomic.Bool
	sweepMu            sync.Mutex
	sweepFrom          []byte // next claim key the bounded sweep examines
}
type admissionError struct{ code string }

func (e *admissionError) Error() string       { return "model credit authorization failed" }
func (e *admissionError) BillingCode() string { return e.code }

var claims = []byte("claims")
var pending = []byte("pending")

// sweepBatch bounds how many claims one Flush examines for expiry.
const sweepBatch = 1024

func key(namespace, id string) []byte { b, _ := json.Marshal([]string{namespace, id}); return b }

func seconds(n int64, fallback time.Duration) (time.Duration, bool) {
	if n == 0 {
		return fallback, true
	}
	return time.Duration(n) * time.Second, n > 0 && n <= math.MaxInt64/int64(time.Second)
}
func New(c Config, tlsIdentity rpc.TLSConfig) (*Manager, error) {
	u, e := url.Parse(c.URL)
	retention, validRetention := seconds(c.RetentionSeconds, 7*24*time.Hour)
	authorizingTimeout, validTimeout := seconds(c.AuthorizingTimeoutSeconds, time.Hour)
	if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || c.Database == "" || !validRetention || !validTimeout {
		return nil, errors.New("invalid meter configuration")
	}
	tlsConfig, e := rpc.ClientTLS(tlsIdentity, c.PeerCertificateFile)
	if e != nil {
		return nil, e
	}
	if e = os.MkdirAll(filepath.Dir(c.Database), 0700); e != nil {
		return nil, e
	}
	db, e := bolt.Open(c.Database, 0600, &bolt.Options{Timeout: time.Second})
	if e != nil {
		return nil, fmt.Errorf("meter database unavailable: %w", e)
	}
	transport := &http.Transport{TLSClientConfig: tlsConfig, MaxIdleConnsPerHost: 4}
	m := &Manager{db: db, url: c.URL, transport: transport, client: &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, retention: retention, authorizingTimeout: authorizingTimeout, observer: c.Observer, stopped: make(chan struct{})}
	e = db.Update(func(tx *bolt.Tx) error {
		for _, name := range [][]byte{claims, pending} {
			if _, e := tx.CreateBucketIfNotExists(name); e != nil {
				return e
			}
		}
		stamped := map[string]record{}
		// A crash after admission but before a durable receipt is ambiguous. Inform
		// the wallet without replaying a model call or automatically releasing funds.
		e := tx.Bucket(claims).ForEach(func(k, v []byte) error {
			var r record
			if json.Unmarshal(v, &r) != nil {
				return errors.New("invalid meter record")
			}
			if r.Updated.IsZero() {
				r.Updated = time.Now()
				stamped[string(k)] = r
			}
			if r.State != "active" {
				return nil
			}
			p := receipt{Namespace: r.Reservation.Namespace, RequestID: r.Reservation.RequestID, Source: r.Reservation.Source, Outcome: "uncertain", Usage: ai.Usage{}}
			raw, _ := json.Marshal(p)
			return tx.Bucket(pending).Put(k, raw)
		})
		for k, r := range stamped {
			raw, _ := json.Marshal(r)
			if e == nil {
				e = tx.Bucket(claims).Put([]byte(k), raw)
			}
		}
		return e
	})
	if e != nil {
		db.Close()
		return nil, e
	}
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-m.stopped:
				return
			case <-ticker.C:
				ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
				expired, e := m.flush(ctx)
				cancel()
				if m.observer != nil {
					report := FlushReport{Pending: m.Pending(), Expired: expired}
					if e != nil {
						report.Error = e.Error()
					}
					m.observer(report)
				}
			}
		}
	}()
	return m, nil
}
func (m *Manager) call(ctx context.Context, method string, params any, out any) error {
	payload, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": "meter-" + time.Now().UTC().Format("20060102T150405.000000000"), "method": method, "params": params})
	var requestID struct {
		ID string `json:"id"`
	}
	json.Unmarshal(payload, &requestID)
	req, e := http.NewRequestWithContext(ctx, "POST", m.url, bytes.NewReader(payload))
	if e != nil {
		return e
	}
	req.Header.Set("Content-Type", "application/json")
	res, e := m.client.Do(req)
	if e != nil {
		return errors.New("wallet transport unavailable")
	}
	defer res.Body.Close()
	body, e := io.ReadAll(io.LimitReader(res.Body, (1<<20)+1))
	if e != nil || len(body) > 1<<20 {
		return errors.New("invalid wallet response")
	}
	var envelope struct {
		JSONRPC string          `json:"jsonrpc"`
		ID      string          `json:"id"`
		Result  json.RawMessage `json:"result"`
		Error   *rpc.Error      `json:"error,omitempty"`
	}
	if rpc.Decode(body, &envelope) != nil || envelope.JSONRPC != "2.0" || envelope.ID != requestID.ID || (len(envelope.Result) > 0) == (envelope.Error != nil) {
		return errors.New("invalid wallet envelope")
	}
	if envelope.Error != nil {
		return envelope.Error
	}
	if res.StatusCode != 200 {
		return errors.New("wallet HTTP error")
	}
	if out != nil && json.Unmarshal(envelope.Result, out) != nil {
		return errors.New("invalid wallet result")
	}
	return nil
}
func (m *Manager) Reserve(ctx context.Context, r gateway.Reservation) (func(gateway.Observation) error, error) {
	if m.unhealthy.Load() {
		return nil, &admissionError{"billing_unavailable"}
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	k := key(r.Namespace, r.RequestID)
	e := m.db.Update(func(tx *bolt.Tx) error {
		if tx.Bucket(claims).Get(k) != nil {
			return &admissionError{"duplicate_request"}
		}
		raw, _ := json.Marshal(record{Reservation: r, State: "authorizing", Updated: time.Now()})
		return tx.Bucket(claims).Put(k, raw)
	})
	if e != nil {
		return nil, e
	}
	var authorization struct {
		ReservationID  string `json:"reservation_id"`
		TariffVersion  int64  `json:"tariff_version"`
		ReservedMicros int64  `json:"reserved_micros"`
		Duplicate      bool   `json:"duplicate"`
	}
	if e = m.call(ctx, "platform.wallet.reserve", r, &authorization); e != nil {
		var re *rpc.Error
		if errors.As(e, &re) && re.Code == -32002 {
			return nil, &admissionError{"insufficient_credits"}
		}
		return nil, &admissionError{"billing_unavailable"}
	}
	if authorization.Duplicate {
		return nil, &admissionError{"duplicate_request"}
	}
	if authorization.ReservationID == "" || authorization.TariffVersion < 1 || authorization.ReservedMicros < 0 {
		return nil, &admissionError{"billing_unavailable"}
	}
	e = m.db.Update(func(tx *bolt.Tx) error {
		raw, _ := json.Marshal(record{Reservation: r, State: "active", Updated: time.Now()})
		return tx.Bucket(claims).Put(k, raw)
	})
	if e != nil {
		m.unhealthy.Store(true)
		return nil, &admissionError{"billing_unavailable"}
	}
	var once sync.Once
	var result error
	return func(obs gateway.Observation) error {
		once.Do(func() {
			outcome := "complete"
			if obs.ErrorCode != "" {
				outcome = "uncertain"
			}
			switch obs.HTTPStatus {
			case 400, 401, 403, 404, 413, 422, 429:
				if !obs.Usage.Known {
					outcome = "rejected"
				}
			}
			p := receipt{Namespace: r.Namespace, RequestID: r.RequestID, Usage: obs.Usage, Outcome: outcome, ProviderStatus: obs.HTTPStatus, Source: r.Source}
			result = m.db.Update(func(tx *bolt.Tx) error {
				raw, _ := json.Marshal(p)
				if e := tx.Bucket(pending).Put(k, raw); e != nil {
					return e
				}
				claim, _ := json.Marshal(record{Reservation: r, State: "recorded", Receipt: &p, Updated: time.Now()})
				return tx.Bucket(claims).Put(k, claim)
			})
			if result != nil {
				m.unhealthy.Store(true)
			}
		})
		return result
	}, nil
}

// Flush retries pending receipts, then removes expired claims.
func (m *Manager) Flush(ctx context.Context) error {
	_, e := m.flush(ctx)
	return e
}
func (m *Manager) flush(ctx context.Context) (int, error) {
	settleErr := m.settlePending(ctx)
	expired, e := m.sweep()
	if settleErr != nil {
		return expired, settleErr
	}
	return expired, e
}

// Pending returns how many receipts the wallet has not acknowledged, or -1 if
// the database cannot be read.
func (m *Manager) Pending() int {
	n := -1
	_ = m.db.View(func(tx *bolt.Tx) error {
		n = tx.Bucket(pending).Stats().KeyN
		return nil
	})
	return n
}

// sweep removes settled claims older than retention and authorizing claims
// older than authorizingTimeout. A recorded claim whose receipt is still
// pending stays, so its request ID keeps being rejected as a duplicate; active
// claims and claims of unknown age are never removed. Each call examines at most sweepBatch claims and
// resumes where the previous call stopped.
func (m *Manager) sweep() (int, error) {
	m.sweepMu.Lock()
	defer m.sweepMu.Unlock()
	now := time.Now()
	var next []byte
	expired := 0
	e := m.db.Update(func(tx *bolt.Tx) error {
		var remove [][]byte
		c := tx.Bucket(claims).Cursor()
		k, v := c.Seek(m.sweepFrom)
		for n := 0; k != nil && n < sweepBatch; k, v = c.Next() {
			n++
			var r record
			if json.Unmarshal(v, &r) != nil {
				return errors.New("invalid meter record")
			}
			limit := map[string]time.Duration{"recorded": m.retention, "authorizing": m.authorizingTimeout}[r.State]
			if limit == 0 || r.Updated.IsZero() || now.Sub(r.Updated) < limit || r.State == "recorded" && tx.Bucket(pending).Get(k) != nil {
				continue
			}
			remove = append(remove, append([]byte(nil), k...))
		}
		next = append([]byte(nil), k...)
		for _, k := range remove {
			if e := tx.Bucket(claims).Delete(k); e != nil {
				return e
			}
		}
		expired = len(remove)
		return nil
	})
	if e != nil {
		return 0, e
	}
	m.sweepFrom = next
	return expired, nil
}
func (m *Manager) settlePending(ctx context.Context) error {
	type item struct{ k, raw []byte }
	items := []item{}
	if e := m.db.View(func(tx *bolt.Tx) error {
		c := tx.Bucket(pending).Cursor()
		for k, v := c.First(); k != nil && len(items) < 128; k, v = c.Next() {
			items = append(items, item{append([]byte(nil), k...), append([]byte(nil), v...)})
		}
		return nil
	}); e != nil {
		return e
	}
	var last error
	for _, item := range items {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		var p receipt
		if json.Unmarshal(item.raw, &p) != nil {
			return errors.New("invalid pending receipt")
		}
		if e := m.call(ctx, "platform.wallet.settle", p, nil); e != nil {
			last = e
			continue
		}
		if e := m.db.Update(func(tx *bolt.Tx) error {
			if !bytes.Equal(tx.Bucket(pending).Get(item.k), item.raw) {
				return nil
			}
			return tx.Bucket(pending).Delete(item.k)
		}); e != nil {
			return e
		}
	}
	return last
}
func (m *Manager) Close() error {
	var e error
	m.once.Do(func() {
		close(m.stopped)
		m.wg.Wait()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		m.Flush(ctx)
		cancel()
		m.transport.CloseIdleConnections()
		e = m.db.Close()
	})
	return e
}
