package main

import (
	"context"
	"io"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

// memStore is an in-memory Store. Like S3, it stores nothing if reading the
// body fails.
type memStore struct {
	mu      sync.Mutex
	objects map[string]memObject
}

type memObject struct {
	data     []byte
	modified time.Time
}

func newMemStore() *memStore {
	return &memStore{objects: map[string]memObject{}}
}

func (m *memStore) Put(ctx context.Context, key string, body io.Reader) error {
	data, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.objects[key] = memObject{data: data, modified: time.Now()}
	return nil
}

func (m *memStore) List(ctx context.Context, prefix string) ([]Object, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var objects []Object
	for key, o := range m.objects {
		if strings.HasPrefix(key, prefix) {
			objects = append(objects, Object{Key: key, LastModified: o.modified})
		}
	}
	return objects, nil
}

func (m *memStore) Delete(ctx context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.objects, key)
	return nil
}

func (m *memStore) keys() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Sorted(maps.Keys(m.objects))
}

func TestPruneKeepsNewest(t *testing.T) {
	store := newMemStore()
	for _, key := range []string{
		"orders/20261101T000000.000000Z.sql.zst",
		"orders/20261103T000000.000000Z.sql.zst",
		"orders/20261102T000000.000000Z.sql.zst",
		"orders/20261104T000000.000000Z.sql.zst",
		"orders-archive/20261101T000000.000000Z.sql.zst",
	} {
		store.objects[key] = memObject{}
	}

	if err := prune(t.Context(), store, &Backup{Name: "orders", Keep: 2}); err != nil {
		t.Fatal(err)
	}

	want := []string{
		"orders-archive/20261101T000000.000000Z.sql.zst",
		"orders/20261103T000000.000000Z.sql.zst",
		"orders/20261104T000000.000000Z.sql.zst",
	}
	if got := store.keys(); !slices.Equal(got, want) {
		t.Errorf("keys after prune = %v, want %v", got, want)
	}
}

func TestPruneWithFewerThanKeep(t *testing.T) {
	store := newMemStore()
	store.objects["orders/a.sql.zst"] = memObject{}

	if err := prune(t.Context(), store, &Backup{Name: "orders", Keep: 5}); err != nil {
		t.Fatal(err)
	}
	if len(store.keys()) != 1 {
		t.Error("prune deleted a dump it should have kept")
	}
}
