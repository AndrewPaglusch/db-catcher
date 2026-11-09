package main

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// TestIntegration runs against a real S3-compatible server. It is skipped
// unless DBCATCHER_TEST_S3_ENDPOINT is set; `make test-integration` starts a
// SeaweedFS server and sets it.
func TestIntegration(t *testing.T) {
	endpoint := os.Getenv("DBCATCHER_TEST_S3_ENDPOINT")
	if endpoint == "" {
		t.Skip("DBCATCHER_TEST_S3_ENDPOINT not set")
	}
	t.Setenv("AWS_ACCESS_KEY_ID", "test")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "test")

	cfg := S3Config{Endpoint: endpoint, Region: "us-east-1", Bucket: "dbcatcher-test"}
	store, err := newS3Store(t.Context(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.client.CreateBucket(t.Context(), &s3.CreateBucketInput{Bucket: aws.String(cfg.Bucket)}); err != nil {
		t.Fatal(err)
	}

	a, orders := newTestApp(t, "http://127.0.0.1:1")
	a.store = store
	server := httptest.NewServer(a.routes())
	defer server.Close()

	// Random hex barely compresses, so this dump is big enough to need a
	// multipart upload even after zstd.
	random := make([]byte, 20<<20)
	rand.Read(random)
	bigDump := append([]byte(hex.EncodeToString(random)), readFixture(t, "postgres.sql")...)
	bigTruncated := bigDump[:len(bigDump)-200]

	t.Run("upload and prune", func(t *testing.T) {
		var keys []string
		for _, dump := range [][]byte{bigDump, readFixture(t, "postgres.sql"), bigDump, readFixture(t, "postgres.sql")} {
			status, key := upload(t, server.URL, ordersID, bytes.NewReader(dump))
			if status != http.StatusOK {
				t.Fatalf("status = %d, want 200", status)
			}
			keys = append(keys, key)
		}

		objects, err := store.List(t.Context(), "orders/")
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, o := range objects {
			got = append(got, o.Key)
		}
		if want := keys[len(keys)-orders.Keep:]; !slices.Equal(got, want) {
			t.Errorf("stored keys = %v, want the newest %d: %v", got, orders.Keep, want)
		}
	})

	t.Run("invalid upload leaves nothing behind", func(t *testing.T) {
		before, err := store.List(t.Context(), "orders/")
		if err != nil {
			t.Fatal(err)
		}

		status, _ := upload(t, server.URL, ordersID, bytes.NewReader(bigTruncated))
		if status != http.StatusUnprocessableEntity {
			t.Fatalf("status = %d, want 422", status)
		}

		after, err := store.List(t.Context(), "orders/")
		if err != nil {
			t.Fatal(err)
		}
		if len(after) != len(before) {
			t.Errorf("object count went from %d to %d", len(before), len(after))
		}
		pending, err := store.client.ListMultipartUploads(t.Context(), &s3.ListMultipartUploadsInput{Bucket: aws.String(cfg.Bucket)})
		if err != nil {
			t.Fatal(err)
		}
		if len(pending.Uploads) != 0 {
			t.Errorf("%d multipart uploads left open", len(pending.Uploads))
		}
	})
}
