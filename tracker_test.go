package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

// webhookRecorder is a webhook endpoint that records the events it receives
// and can be told to fail.
type webhookRecorder struct {
	mu     sync.Mutex
	events []string
	broken bool
	server *httptest.Server
}

func newWebhookRecorder(t *testing.T) *webhookRecorder {
	rec := &webhookRecorder{}
	rec.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.mu.Lock()
		defer rec.mu.Unlock()
		if rec.broken {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		var msg alert
		if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
			t.Errorf("webhook got invalid JSON: %v", err)
		}
		rec.events = append(rec.events, msg.Event)
	}))
	t.Cleanup(rec.server.Close)
	return rec
}

func (rec *webhookRecorder) received() []string {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	return append([]string(nil), rec.events...)
}

func (rec *webhookRecorder) setBroken(broken bool) {
	rec.mu.Lock()
	defer rec.mu.Unlock()
	rec.broken = broken
}

// newTestApp returns an app with one postgres backup named "orders" that
// alerts after 60 minutes, backed by an in-memory store.
func newTestApp(t *testing.T, webhook string) (*app, *Backup) {
	t.Helper()
	cfg, err := parseConfig([]byte(`
s3: {region: test, bucket: test}
notify_webhook: ` + webhook + `
backups:
  - id: 3f6c2a9e-1111-4000-8000-000000000001
    name: orders
    type: postgres
    keep: 2
    alert_after_minutes: 60
`))
	if err != nil {
		t.Fatal(err)
	}
	a := &app{store: newMemStore(), tracker: newTracker()}
	if err := a.tracker.sync(t.Context(), a.store, cfg.Backups); err != nil {
		t.Fatal(err)
	}
	a.config.Store(cfg)
	return a, cfg.Backups[0]
}

func TestOverdueAndRecovery(t *testing.T) {
	webhook := newWebhookRecorder(t)
	a, orders := newTestApp(t, webhook.server.URL)
	start := time.Now()
	a.tracker.recordValid(orders.Name, start)

	a.checkOverdue(start.Add(30 * time.Minute))
	if got := webhook.received(); len(got) != 0 {
		t.Fatalf("sent %v while still healthy", got)
	}

	a.checkOverdue(start.Add(61 * time.Minute))
	a.checkOverdue(start.Add(62 * time.Minute))
	a.checkOverdue(start.Add(120 * time.Minute))
	if got := webhook.received(); len(got) != 1 || got[0] != "overdue" {
		t.Fatalf("sent %v, want a single overdue notice", got)
	}

	a.tracker.recordValid(orders.Name, start.Add(130*time.Minute))
	a.checkOverdue(start.Add(131 * time.Minute))
	a.checkOverdue(start.Add(132 * time.Minute))
	if got := webhook.received(); len(got) != 2 || got[1] != "recovered" {
		t.Fatalf("sent %v, want overdue then a single recovered notice", got)
	}
}

func TestFailedNoticeIsRetried(t *testing.T) {
	webhook := newWebhookRecorder(t)
	a, orders := newTestApp(t, webhook.server.URL)
	start := time.Now()
	a.tracker.recordValid(orders.Name, start)

	webhook.setBroken(true)
	a.checkOverdue(start.Add(61 * time.Minute))
	webhook.setBroken(false)
	a.checkOverdue(start.Add(62 * time.Minute))

	if got := webhook.received(); len(got) != 1 || got[0] != "overdue" {
		t.Fatalf("sent %v, want the overdue notice delivered on retry", got)
	}
}

func TestOneUploadAtATime(t *testing.T) {
	tr := newTracker()
	tr.states["orders"] = &backupState{}

	if !tr.startUpload("orders") {
		t.Fatal("first upload was refused")
	}
	if tr.startUpload("orders") {
		t.Fatal("second concurrent upload was allowed")
	}
	tr.finishUpload("orders")
	if !tr.startUpload("orders") {
		t.Fatal("upload refused after the previous one finished")
	}
}

func TestSyncSeedsFromStore(t *testing.T) {
	store := newMemStore()
	stored := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	store.objects["orders/old.sql.zst"] = memObject{modified: stored.Add(-time.Hour)}
	store.objects["orders/new.sql.zst"] = memObject{modified: stored}

	tr := newTracker()
	backups := []*Backup{{Name: "orders"}, {Name: "billing"}}
	before := time.Now()
	if err := tr.sync(t.Context(), store, backups); err != nil {
		t.Fatal(err)
	}

	if got, _ := tr.status("orders"); !got.Equal(stored) {
		t.Errorf("orders last valid = %v, want newest stored dump %v", got, stored)
	}
	if got, _ := tr.status("billing"); got.Before(before) {
		t.Errorf("billing last valid = %v, want the time of sync since it has no dumps", got)
	}

	tr.setNotifiedOverdue("orders", true)
	if err := tr.sync(t.Context(), store, backups[:1]); err != nil {
		t.Fatal(err)
	}
	if _, notified := tr.status("orders"); !notified {
		t.Error("sync reset the state of a backup that was already tracked")
	}
	if tr.lookup("billing") != nil {
		t.Error("sync kept a backup that was removed from the config")
	}
}
