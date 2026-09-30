package meter

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"easygo-agent/services/ai-gateway/ai"
	"easygo-agent/services/ai-gateway/gateway"
	bolt "go.etcd.io/bbolt"
)

// putClaim writes a claim directly; a zero updated time omits the field, as
// claims written before it existed do.
func putClaim(t *testing.T, db *bolt.DB, id, state string, updated time.Time, withPending bool) {
	t.Helper()
	k := key("user-one", id)
	e := db.Update(func(tx *bolt.Tx) error {
		if withPending {
			p, _ := json.Marshal(receipt{Namespace: "user-one", RequestID: id, Outcome: "complete", Source: "gateway.generate"})
			if e := tx.Bucket(pending).Put(k, p); e != nil {
				return e
			}
		}
		raw, _ := json.Marshal(record{Reservation: gateway.Reservation{Namespace: "user-one", RequestID: id, Source: "gateway.generate"}, State: state, Updated: updated})
		return tx.Bucket(claims).Put(k, raw)
	})
	if e != nil {
		t.Fatal(e)
	}
}
func storedClaims(t *testing.T, db *bolt.DB) map[string]record {
	t.Helper()
	out := map[string]record{}
	if e := db.View(func(tx *bolt.Tx) error {
		return tx.Bucket(claims).ForEach(func(_, v []byte) error {
			var r record
			if e := json.Unmarshal(v, &r); e != nil {
				return e
			}
			out[r.Reservation.RequestID] = r
			return nil
		})
	}); e != nil {
		t.Fatal(e)
	}
	return out
}

func TestFlushRemovesExpiredClaimsOnly(t *testing.T) {
	wallet := newWallet(t)
	wallet.loseAck = true // keep the pending receipt below unsettled
	m := newMeter(t, wallet)
	defer m.Close()
	now := time.Now()
	putClaim(t, m.db, "recorded-expired", "recorded", now.Add(-8*24*time.Hour), false)
	putClaim(t, m.db, "recorded-expired-unsettled", "recorded", now.Add(-8*24*time.Hour), true)
	putClaim(t, m.db, "recorded-fresh", "recorded", now.Add(-6*24*time.Hour), false)
	putClaim(t, m.db, "authorizing-expired", "authorizing", now.Add(-2*time.Hour), false)
	putClaim(t, m.db, "authorizing-fresh", "authorizing", now.Add(-time.Minute), false)
	putClaim(t, m.db, "active-old", "active", now.Add(-30*24*time.Hour), false)
	if e := m.Flush(context.Background()); e == nil {
		t.Fatal("lost settlement ack not modeled")
	}
	got := storedClaims(t, m.db)
	for _, id := range []string{"recorded-expired", "authorizing-expired"} {
		if _, ok := got[id]; ok {
			t.Errorf("%s not removed", id)
		}
	}
	for _, id := range []string{"recorded-expired-unsettled", "recorded-fresh", "authorizing-fresh", "active-old"} {
		if _, ok := got[id]; !ok {
			t.Errorf("%s removed", id)
		}
	}
	if n := m.Pending(); n != 1 {
		t.Fatalf("Pending() = %d, want 1", n)
	}
}

func TestSweepIsBoundedAndResumes(t *testing.T) {
	// A Manager without New has no background flush, so each sweep is observable.
	db, e := bolt.Open(filepath.Join(t.TempDir(), "meter.db"), 0600, &bolt.Options{Timeout: time.Second})
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	now := time.Now()
	// Keys sort by request ID: a full batch of fresh claims, then expired ones.
	e = db.Update(func(tx *bolt.Tx) error {
		for _, name := range [][]byte{claims, pending} {
			if _, e := tx.CreateBucket(name); e != nil {
				return e
			}
		}
		for i := 0; i < sweepBatch+10; i++ {
			id, updated := fmt.Sprintf("a-%04d", i), now
			if i >= sweepBatch {
				id, updated = fmt.Sprintf("b-%04d", i), now.Add(-2*time.Hour)
			}
			raw, _ := json.Marshal(record{Reservation: gateway.Reservation{Namespace: "user-one", RequestID: id}, State: "authorizing", Updated: updated})
			if e := tx.Bucket(claims).Put(key("user-one", id), raw); e != nil {
				return e
			}
		}
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	m := &Manager{db: db, retention: 7 * 24 * time.Hour, authorizingTimeout: time.Hour}
	for i, want := range []int{0, 10, 0} {
		if removed, e := m.sweep(); e != nil || removed != want {
			t.Fatalf("sweep %d removed %d, want %d: %v", i+1, removed, want, e)
		}
	}
	if n := len(storedClaims(t, db)); n != sweepBatch {
		t.Fatalf("%d claims left, want %d fresh", n, sweepBatch)
	}
}

func TestPendingAndObserverReports(t *testing.T) {
	wallet := newWallet(t)
	wallet.loseAck = true
	reports := make(chan FlushReport, 64)
	wallet.config.Observer = func(r FlushReport) {
		select {
		case reports <- r:
		default:
		}
	}
	m := newMeter(t, wallet)
	defer m.Close()
	settle, e := m.Reserve(context.Background(), gateway.Reservation{Namespace: "user-one", RequestID: "observed", Fingerprint: "abc", Model: "chat", Source: "gateway.generate", InputTokens: 10, OutputTokens: 32})
	if e != nil {
		t.Fatal(e)
	}
	if e = settle(gateway.Observation{Usage: ai.Usage{Known: true, InputTokens: 10, OutputTokens: 3}}); e != nil {
		t.Fatal(e)
	}
	if n := m.Pending(); n != 1 {
		t.Fatalf("Pending() = %d, want 1", n)
	}
	wait := func(want func(FlushReport) bool) FlushReport {
		t.Helper()
		deadline := time.After(5 * time.Second)
		for {
			select {
			case r := <-reports:
				if want(r) {
					return r
				}
			case <-deadline:
				t.Fatal("no matching flush report")
			}
		}
	}
	wait(func(r FlushReport) bool { return r.Pending == 1 && r.Error != "" })
	wallet.mu.Lock()
	wallet.loseAck = false
	wallet.mu.Unlock()
	wait(func(r FlushReport) bool { return r.Pending == 0 && r.Error == "" })
	if n := m.Pending(); n != 0 {
		t.Fatalf("Pending() = %d after settlement", n)
	}
}

func TestLegacyClaimsStampedAndActiveStillUncertain(t *testing.T) {
	wallet := newWallet(t)
	if e := newMeter(t, wallet).Close(); e != nil {
		t.Fatal(e)
	}
	// Write the claims as the previous binary left them, with no meter running.
	db, e := bolt.Open(wallet.config.Database, 0600, &bolt.Options{Timeout: time.Second})
	if e != nil {
		t.Fatal(e)
	}
	putClaim(t, db, "legacy-recorded", "recorded", time.Time{}, false)
	putClaim(t, db, "legacy-authorizing", "authorizing", time.Time{}, false)
	putClaim(t, db, "legacy-active", "active", time.Time{}, false)
	if e = db.Close(); e != nil {
		t.Fatal(e)
	}
	before := time.Now()
	m := newMeter(t, wallet)
	defer m.Close()
	if e = m.Flush(context.Background()); e != nil {
		t.Fatal(e)
	}
	got := storedClaims(t, m.db)
	for _, id := range []string{"legacy-recorded", "legacy-authorizing", "legacy-active"} {
		if r, ok := got[id]; !ok || r.Updated.Before(before) {
			t.Errorf("%s: present=%v updated=%v, want kept and stamped at startup", id, ok, r.Updated)
		}
	}
	wallet.mu.Lock()
	defer wallet.mu.Unlock()
	if r := wallet.settled["legacy-active"]; r.Outcome != "uncertain" || r.Usage.Known {
		t.Fatalf("active claim after restart: %+v", r)
	}
}

func TestRetentionConfig(t *testing.T) {
	wallet := newWallet(t)
	m := newMeter(t, wallet)
	if m.retention != 7*24*time.Hour || m.authorizingTimeout != time.Hour {
		t.Fatalf("defaults: %v %v", m.retention, m.authorizingTimeout)
	}
	m.Close()
	c := wallet.config
	c.RetentionSeconds, c.AuthorizingTimeoutSeconds = 60, 30
	if m, e := New(c, wallet.identity); e != nil || m.retention != time.Minute || m.authorizingTimeout != 30*time.Second {
		t.Fatalf("explicit seconds: %v", e)
	} else {
		m.Close()
	}
	for _, bad := range []Config{{RetentionSeconds: -1}, {AuthorizingTimeoutSeconds: -1}, {RetentionSeconds: 1 << 62}} {
		c := wallet.config
		c.RetentionSeconds, c.AuthorizingTimeoutSeconds = bad.RetentionSeconds, bad.AuthorizingTimeoutSeconds
		if m, e := New(c, wallet.identity); e == nil {
			m.Close()
			t.Fatalf("accepted %+v", bad)
		}
	}
}

func TestInvalidWalletURL(t *testing.T) {
	wallet := newWallet(t)
	for _, u := range []string{"http://wallet/rpc", "https:///rpc", "https://u:p@wallet/rpc", "https://wallet/rpc?x=1", "https://wallet/rpc#f"} {
		c := wallet.config
		c.URL = u
		if m, e := New(c, wallet.identity); e == nil || e.Error() != "invalid meter configuration" {
			if m != nil {
				m.Close()
			}
			t.Errorf("%s => %v", u, e)
		}
	}
}
