package sidecar

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

var ErrDurabilityUncertain = errors.New("write applied but durability is uncertain")

type Operation interface {
	TargetThread() string
	Apply(*Document) error
}

type SetStatusOperation struct {
	ThreadID string
	Status   string
}

func (operation SetStatusOperation) TargetThread() string {
	return operation.ThreadID
}

func (operation SetStatusOperation) Apply(document *Document) error {
	return document.SetStatus(operation.ThreadID, operation.Status)
}

type AppendMessageOperation struct {
	ThreadID string
	Message  Message
}

func (operation AppendMessageOperation) TargetThread() string {
	return operation.ThreadID
}

func (operation AppendMessageOperation) Apply(document *Document) error {
	return document.AppendMessage(operation.ThreadID, operation.Message)
}

type Hooks struct {
	BeforeTempSync  func(attempt int) error
	BeforeFinalRead func(attempt int)
	AfterFinalRead  func(attempt int)
	BeforeDirSync   func() error
}

type Store struct {
	mu       sync.Mutex
	MaxTries int
	Hooks    Hooks
}

func (store *Store) Mutate(
	path string,
	expectedTargetFingerprint string,
	operation Operation,
) ([]byte, error) {
	store.mu.Lock()
	defer store.mu.Unlock()

	maxTries := store.MaxTries
	if maxTries <= 0 {
		maxTries = 3
	}

	for attempt := 1; attempt <= maxTries; attempt++ {
		before, err := readBounded(path)
		if err != nil {
			return nil, err
		}
		document, err := Decode(before)
		if err != nil {
			return nil, err
		}
		fingerprint, err := document.ThreadFingerprint(operation.TargetThread())
		if err != nil {
			return nil, err
		}
		if fingerprint != expectedTargetFingerprint {
			return nil, ErrTargetChanged
		}
		if err := operation.Apply(document); err != nil {
			return nil, err
		}
		updated, err := document.Bytes()
		if err != nil {
			return nil, err
		}

		info, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		directory := filepath.Dir(path)
		temporary, err := os.CreateTemp(directory, "."+filepath.Base(path)+".tmp-*")
		if err != nil {
			return nil, err
		}
		temporaryPath := temporary.Name()
		renamed := false
		cleanup := func() {
			temporary.Close()
			if !renamed {
				_ = os.Remove(temporaryPath)
			}
		}

		if err := temporary.Chmod(info.Mode().Perm()); err != nil {
			cleanup()
			return nil, err
		}
		if _, err := temporary.Write(updated); err != nil {
			cleanup()
			return nil, err
		}
		if store.Hooks.BeforeTempSync != nil {
			if err := store.Hooks.BeforeTempSync(attempt); err != nil {
				cleanup()
				return nil, err
			}
		}
		if err := temporary.Sync(); err != nil {
			cleanup()
			return nil, err
		}
		if err := temporary.Close(); err != nil {
			cleanup()
			return nil, err
		}

		if store.Hooks.BeforeFinalRead != nil {
			store.Hooks.BeforeFinalRead(attempt)
		}
		current, err := readBounded(path)
		if err != nil {
			cleanup()
			return nil, err
		}
		if sha256.Sum256(current) != sha256.Sum256(before) {
			cleanup()
			continue
		}
		if store.Hooks.AfterFinalRead != nil {
			store.Hooks.AfterFinalRead(attempt)
		}
		if err := os.Rename(temporaryPath, path); err != nil {
			cleanup()
			return nil, err
		}
		renamed = true

		if store.Hooks.BeforeDirSync != nil {
			if err := store.Hooks.BeforeDirSync(); err != nil {
				return updated, fmt.Errorf("%w: %v", ErrDurabilityUncertain, err)
			}
		}
		dir, err := os.Open(directory)
		if err != nil {
			return updated, fmt.Errorf("%w: %v", ErrDurabilityUncertain, err)
		}
		syncErr := dir.Sync()
		closeErr := dir.Close()
		if syncErr != nil {
			return updated, fmt.Errorf("%w: %v", ErrDurabilityUncertain, syncErr)
		}
		if closeErr != nil {
			return updated, fmt.Errorf("%w: %v", ErrDurabilityUncertain, closeErr)
		}
		return updated, nil
	}
	return nil, fmt.Errorf("sidecar kept changing after %d attempts", maxTries)
}

func readBounded(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	reader := io.LimitReader(file, MaxBytes+1)
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	if len(data) > MaxBytes {
		return nil, fmt.Errorf("%w: sidecar exceeds %d bytes", ErrReadOnly, MaxBytes)
	}
	return data, nil
}
