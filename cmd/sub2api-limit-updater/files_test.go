package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCopyWithContextStopsBeforeWritingCanceledChunk(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	source := &cancelAfterRead{cancel: cancel}
	var destination bytes.Buffer
	if err := copyWithContext(ctx, &destination, source); !errors.Is(err, context.Canceled) {
		t.Fatalf("copyWithContext error = %v, want context.Canceled", err)
	}
	if destination.Len() != 0 {
		t.Fatalf("destination received %d bytes after cancellation", destination.Len())
	}
}

func TestAtomicCopyContextLeavesDestinationOnCancellation(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	source := filepath.Join(directory, "source")
	destination := filepath.Join(directory, "destination")
	writeTestFile(t, source, "old", 0600)
	writeTestFile(t, destination, "current", 0600)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := atomicCopyContext(ctx, source, destination, 0600, 0, 0, false); !errors.Is(err, context.Canceled) {
		t.Fatalf("atomicCopyContext error = %v, want context.Canceled", err)
	}
	body, err := os.ReadFile(destination)
	if err != nil || string(body) != "current" {
		t.Fatalf("destination = %q, %v; want unchanged", body, err)
	}
}

type cancelAfterRead struct {
	cancel context.CancelFunc
	done   bool
}

func (reader *cancelAfterRead) Read(buffer []byte) (int, error) {
	if reader.done {
		return 0, nil
	}
	reader.done = true
	count := copy(buffer, "partial")
	reader.cancel()
	return count, nil
}
