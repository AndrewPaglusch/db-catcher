package main

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type app struct {
	config  atomic.Pointer[Config]
	store   Store
	tracker *tracker
}

func (a *app) routes() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("PUT /upload", a.handleUpload)
	mux.HandleFunc("POST /upload", a.handleUpload)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
	})
	return mux
}

func (a *app) handleUpload(w http.ResponseWriter, r *http.Request) {
	token, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	backup := a.config.Load().backupFor(token)
	if backup == nil {
		log.Printf("rejected unauthorized upload from %s", r.RemoteAddr)
		w.WriteHeader(http.StatusUnauthorized)
		return
	}

	if !a.tracker.startUpload(backup.Name) {
		log.Printf("%s: rejected upload, another one is in progress", backup.Name)
		http.Error(w, "an upload for this backup is already in progress", http.StatusConflict)
		return
	}
	defer a.tracker.finishUpload(backup.Name)

	started := time.Now()
	key := fmt.Sprintf("%s/%s.sql.zst", backup.Name, started.UTC().Format("20060102T150405.000000Z"))
	dump := newDumpValidator(r.Body, backup.engine.marker)
	err := a.storeCompressed(r.Context(), key, dump)

	switch {
	case dump.invalid():
		log.Printf("%s: rejected dump: %v", backup.Name, dump.err)
		if backup.AlertOnInvalid {
			a.alertInvalid(backup, dump.err)
		}
		http.Error(w, dump.err.Error(), http.StatusUnprocessableEntity)
		return
	case dump.err != nil:
		log.Printf("%s: upload interrupted after %d bytes: %v", backup.Name, dump.size, dump.err)
		http.Error(w, "upload interrupted", http.StatusBadRequest)
		return
	case err != nil:
		log.Printf("%s: storing %s failed: %v", backup.Name, key, err)
		http.Error(w, "storage error", http.StatusBadGateway)
		return
	}

	a.tracker.recordValid(backup.Name, time.Now())
	log.Printf("%s: stored %s (%d bytes in %s)", backup.Name, key, dump.size, time.Since(started).Round(time.Millisecond))

	// The dump is already safely stored, so a failed prune is only logged.
	// The next successful upload will try again.
	if err := prune(r.Context(), a.store, backup); err != nil {
		log.Printf("%s: pruning old dumps: %v", backup.Name, err)
	}
	fmt.Fprintln(w, key)
}

// storeCompressed streams src through zstd into the object at key. If reading
// src fails, the store sees a failed read and abandons the object.
func (a *app) storeCompressed(ctx context.Context, key string, src io.Reader) error {
	pr, pw := io.Pipe()
	var wg sync.WaitGroup
	wg.Go(func() { pw.CloseWithError(compress(pw, src)) })

	err := a.store.Put(ctx, key, pr)
	pr.Close() // unblocks the compressor if the store stopped reading early
	wg.Wait()
	return err
}
