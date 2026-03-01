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

func TestPauseResumeConversionJob(t *testing.T) {
	store := openTempStore(t)
	defer store.Close()
	ctx := context.Background()

	inserted, err := store.EnqueueConversion(ctx, "hash1", "/in/a.mkv", "/out/a.mp4", "h264")
	if err != nil || !inserted {
		t.Fatalf("enqueue conversion: inserted=%v err=%v", inserted, err)
	}
	jobs, err := store.ListConversionJobs(ctx)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("list conversion jobs: len=%d err=%v", len(jobs), err)
	}

	ok, err := store.PauseConversionJob(ctx, jobs[0].ID)
	if err != nil || !ok {
		t.Fatalf("pause conversion: ok=%v err=%v", ok, err)
	}
	jobs, err = store.ListConversionJobs(ctx)
	if err != nil {
		t.Fatalf("list conversion after pause: %v", err)
	}
	if jobs[0].Status != "paused" {
		t.Fatalf("expected paused conversion status, got %s", jobs[0].Status)
	}

	ok, err = store.ResumeConversionJob(ctx, jobs[0].ID)
	if err != nil || !ok {
		t.Fatalf("resume conversion: ok=%v err=%v", ok, err)
	}
	jobs, err = store.ListConversionJobs(ctx)
	if err != nil {
		t.Fatalf("list conversion after resume: %v", err)
	}
	if jobs[0].Status != "queued" {
		t.Fatalf("expected queued conversion status, got %s", jobs[0].Status)
	}
}

func TestPauseResumeUploadJob(t *testing.T) {
	store := openTempStore(t)
	defer store.Close()
	ctx := context.Background()

	inserted, err := store.EnqueueUpload(ctx, "/tmp/a.mp4", "2026/03/01/a.mp4")
	if err != nil || !inserted {
		t.Fatalf("enqueue upload: inserted=%v err=%v", inserted, err)
	}
	jobs, err := store.ListUploadJobs(ctx)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("list upload jobs: len=%d err=%v", len(jobs), err)
	}

	ok, err := store.PauseUploadJob(ctx, jobs[0].ID)
	if err != nil || !ok {
		t.Fatalf("pause upload: ok=%v err=%v", ok, err)
	}
	jobs, err = store.ListUploadJobs(ctx)
	if err != nil {
		t.Fatalf("list upload after pause: %v", err)
	}
	if jobs[0].Status != "paused" {
		t.Fatalf("expected paused upload status, got %s", jobs[0].Status)
	}

	ok, err = store.ResumeUploadJob(ctx, jobs[0].ID)
	if err != nil || !ok {
		t.Fatalf("resume upload: ok=%v err=%v", ok, err)
	}
	jobs, err = store.ListUploadJobs(ctx)
	if err != nil {
		t.Fatalf("list upload after resume: %v", err)
	}
	if jobs[0].Status != "queued" {
		t.Fatalf("expected queued upload status, got %s", jobs[0].Status)
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
