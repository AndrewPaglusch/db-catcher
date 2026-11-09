package main

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// tracker holds the runtime state of every configured backup. It is the only
// state the service keeps, and it can be rebuilt from the bucket at any time.
type tracker struct {
	mu     sync.Mutex
	states map[string]*backupState
}

type backupState struct {
	lastValid       time.Time
	notifiedOverdue bool
	uploading       bool
}

func newTracker() *tracker {
	return &tracker{states: map[string]*backupState{}}
}

// sync makes the tracked backups match backups. Backups already tracked keep
// their state. New ones start from their newest dump in the store, or from
// now if they have none.
func (t *tracker) sync(ctx context.Context, store Store, backups []*Backup) error {
	states := make(map[string]*backupState, len(backups))
	for _, b := range backups {
		if s := t.lookup(b.Name); s != nil {
			states[b.Name] = s
			continue
		}
		lastValid, err := newestDump(ctx, store, b.Name)
		if err != nil {
			return fmt.Errorf("%s: listing existing dumps: %w", b.Name, err)
		}
		if lastValid.IsZero() {
			lastValid = time.Now()
		}
		states[b.Name] = &backupState{lastValid: lastValid}
	}

	t.mu.Lock()
	t.states = states
	t.mu.Unlock()
	return nil
}

func (t *tracker) lookup(name string) *backupState {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.states[name]
}

// startUpload claims name for one upload. It returns false if another upload
// of the same backup is still running.
func (t *tracker) startUpload(name string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.states[name]
	if s == nil || s.uploading {
		return false
	}
	s.uploading = true
	return true
}

func (t *tracker) finishUpload(name string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if s := t.states[name]; s != nil {
		s.uploading = false
	}
}

func (t *tracker) recordValid(name string, at time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if s := t.states[name]; s != nil {
		s.lastValid = at
	}
}

func (t *tracker) status(name string) (lastValid time.Time, notifiedOverdue bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if s := t.states[name]; s != nil {
		return s.lastValid, s.notifiedOverdue
	}
	return time.Time{}, false
}

func (t *tracker) setNotifiedOverdue(name string, notified bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if s := t.states[name]; s != nil {
		s.notifiedOverdue = notified
	}
}
