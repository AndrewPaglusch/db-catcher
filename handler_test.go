package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const ordersID = "3f6c2a9e-1111-4000-8000-000000000001"

func upload(t *testing.T, url, token string, body io.Reader) (int, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPut, url+"/upload", body)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, strings.TrimSpace(string(respBody))
}

func TestUploadStoresValidDump(t *testing.T) {
	a, _ := newTestApp(t, "http://127.0.0.1:1")
	server := httptest.NewServer(a.routes())
	defer server.Close()
	dump := readFixture(t, "postgres.sql")

	status, key := upload(t, server.URL, ordersID, bytes.NewReader(dump))

	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if !strings.HasPrefix(key, "orders/") || !strings.HasSuffix(key, ".sql.zst") {
		t.Errorf("key = %q, want orders/<time>.sql.zst", key)
	}
	stored := a.store.(*memStore).objects[key]
	if !bytes.Equal(decompress(t, stored.data), dump) {
		t.Error("stored object does not decompress to the uploaded dump")
	}
	if lastValid, _ := a.tracker.status("orders"); time.Since(lastValid) > time.Minute {
		t.Error("upload did not update the last valid time")
	}
}

func TestUploadPrunesToKeep(t *testing.T) {
	a, orders := newTestApp(t, "http://127.0.0.1:1")
	server := httptest.NewServer(a.routes())
	defer server.Close()
	dump := readFixture(t, "postgres.sql")

	var keys []string
	for range orders.Keep + 2 {
		status, key := upload(t, server.URL, ordersID, bytes.NewReader(dump))
		if status != http.StatusOK {
			t.Fatalf("status = %d, want 200", status)
		}
		keys = append(keys, key)
	}

	got := a.store.(*memStore).keys()
	want := keys[len(keys)-orders.Keep:]
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("stored keys = %v, want the newest %d: %v", got, orders.Keep, want)
	}
}

func TestUploadRejects(t *testing.T) {
	dump := readFixture(t, "postgres.sql")
	tests := []struct {
		name       string
		token      string
		body       []byte
		wantStatus int
	}{
		{"no token", "", dump, http.StatusUnauthorized},
		{"unknown token", "9b1d2c3e-2222-4000-9000-000000000002", dump, http.StatusUnauthorized},
		{"empty dump", ordersID, nil, http.StatusUnprocessableEntity},
		{"truncated dump", ordersID, dump[:len(dump)/2], http.StatusUnprocessableEntity},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, _ := newTestApp(t, "http://127.0.0.1:1")
			server := httptest.NewServer(a.routes())
			defer server.Close()
			before, _ := a.tracker.status("orders")

			status, _ := upload(t, server.URL, tt.token, bytes.NewReader(tt.body))

			if status != tt.wantStatus {
				t.Errorf("status = %d, want %d", status, tt.wantStatus)
			}
			if keys := a.store.(*memStore).keys(); len(keys) != 0 {
				t.Errorf("rejected upload left objects behind: %v", keys)
			}
			if after, _ := a.tracker.status("orders"); !after.Equal(before) {
				t.Error("rejected upload changed the last valid time")
			}
		})
	}
}

func TestInvalidDumpAlertWhenEnabled(t *testing.T) {
	webhook := newWebhookRecorder(t)
	a, orders := newTestApp(t, webhook.server.URL)
	orders.AlertOnInvalid = true
	server := httptest.NewServer(a.routes())
	defer server.Close()

	upload(t, server.URL, ordersID, strings.NewReader("CREATE TABLE half_a_dump"))

	if got := webhook.received(); len(got) != 1 || got[0] != "invalid" {
		t.Errorf("sent %v, want one invalid notice", got)
	}
}

func TestConcurrentUploadConflicts(t *testing.T) {
	a, _ := newTestApp(t, "http://127.0.0.1:1")
	server := httptest.NewServer(a.routes())
	defer server.Close()

	slowBody, writer := io.Pipe()
	firstStatus := make(chan int)
	go func() {
		status, _ := upload(t, server.URL, ordersID, slowBody)
		firstStatus <- status
	}()
	writer.Write([]byte("-- start of a dump\n"))
	waitForUpload(t, a, "orders")

	status, _ := upload(t, server.URL, ordersID, strings.NewReader("anything"))
	if status != http.StatusConflict {
		t.Errorf("second upload status = %d, want 409", status)
	}

	writer.Write(readFixture(t, "postgres.sql"))
	writer.Close()
	if status := <-firstStatus; status != http.StatusOK {
		t.Errorf("first upload status = %d, want 200", status)
	}
}

func waitForUpload(t *testing.T, a *app, name string) {
	t.Helper()
	for range 1000 {
		a.tracker.mu.Lock()
		uploading := a.tracker.states[name].uploading
		a.tracker.mu.Unlock()
		if uploading {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("upload never started")
}
