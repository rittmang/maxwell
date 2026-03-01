package queue

import (
	"context"
	"path/filepath"
	"testing"

	"maxwell/internal/config"
)

func TestEnqueueDeduplication(t *testing.T) {
	store := openTempStore(t)
	defer store.Close()
	ctx := context.Background()

	inserted, err := store.EnqueueConversion(ctx, "hash1", "/in/a.mkv", "/out/a.mp4", "h264")
	if err != nil {
		t.Fatal(err)
	}
	if !inserted {
		t.Fatalf("first insert should be true")
	}
	inserted, err = store.EnqueueConversion(ctx, "hash1", "/in/a.mkv", "/out/a.mp4", "h264")
	if err != nil {
		t.Fatal(err)
	}
	if inserted {
		t.Fatalf("duplicate insert should be ignored")
	}

	jobs, err := store.ListConversionJobs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected 1 conversion job, got %d", len(jobs))
	}
}

func TestUploadCompletionCreatesLink(t *testing.T) {
	store := openTempStore(t)
	defer store.Close()
	ctx := context.Background()

	inserted, err := store.EnqueueUpload(ctx, "/tmp/a.mp4", "2026/02/28/a.mp4")
	if err != nil {
		t.Fatal(err)
	}
	if !inserted {
		t.Fatalf("expected new upload job")
	}

	job, err := store.NextQueuedUpload(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.MarkUploadRunning(ctx, job.ID, 1); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkUploadDone(ctx, job.ID, "https://example.com/a.mp4"); err != nil {
		t.Fatal(err)
	}

	links, err := store.ListLinks(ctx, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(links) != 1 {
		t.Fatalf("expected one link, got %d", len(links))
	}
	if links[0].FinalURL != "https://example.com/a.mp4" {
		t.Fatalf("unexpected url: %s", links[0].FinalURL)
	}
}

func openTempStore(t *testing.T) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "maxwell.db")
	store, err := Open(config.StateStoreConfig{
		Driver:       "sqlite",
		DSN:          path,
		MaxOpenConns: 1,
	})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	return store
}
