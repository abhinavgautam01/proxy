package database

import (
	"bytes"
	"log/slog"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

var discardLogger = slog.New(slog.DiscardHandler)

func seedHitTestArtifact(t *testing.T, db *DB) (string, string) {
	t.Helper()
	versionPURL, filename := "pkg:npm/lodash@4.17.21", "lodash-4.17.21.tgz"
	if err := db.UpsertPackage(&Package{PURL: "pkg:npm/lodash", Ecosystem: "npm", Name: "lodash"}); err != nil {
		t.Fatalf("UpsertPackage failed: %v", err)
	}
	if err := db.UpsertVersion(&Version{PURL: versionPURL, PackagePURL: "pkg:npm/lodash"}); err != nil {
		t.Fatalf("UpsertVersion failed: %v", err)
	}
	if err := db.UpsertArtifact(&Artifact{
		VersionPURL: versionPURL,
		Filename:    filename,
		UpstreamURL: "https://registry.npmjs.org/lodash/-/" + filename,
	}); err != nil {
		t.Fatalf("UpsertArtifact failed: %v", err)
	}
	return versionPURL, filename
}

func hitCount(t *testing.T, db *DB, versionPURL, filename string) int64 {
	t.Helper()
	a, err := db.GetArtifact(versionPURL, filename)
	if err != nil || a == nil {
		t.Fatalf("GetArtifact failed: %v", err)
	}
	return a.HitCount
}

func TestBatchHitsWritesOnFlush(t *testing.T) {
	runWithBothDatabases(t, func(t *testing.T, db *DB) {
		versionPURL, filename := seedHitTestArtifact(t, db)
		db.BatchHits(time.Hour, discardLogger)

		for range 3 {
			if err := db.RecordArtifactHit(versionPURL, filename); err != nil {
				t.Fatalf("RecordArtifactHit failed: %v", err)
			}
		}
		if got := hitCount(t, db, versionPURL, filename); got != 0 {
			t.Fatalf("hit count before flush = %d, want 0", got)
		}

		if err := db.flushHits(); err != nil {
			t.Fatalf("flushHits failed: %v", err)
		}
		a, err := db.GetArtifact(versionPURL, filename)
		if err != nil {
			t.Fatalf("GetArtifact failed: %v", err)
		}
		if a.HitCount != 3 {
			t.Errorf("hit count after flush = %d, want 3", a.HitCount)
		}
		if !a.LastAccessedAt.Valid {
			t.Error("expected last_accessed_at to be set")
		}
	})
}

func TestBatchHitsWritesOnClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	db, err := Create(path)
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}
	versionPURL, filename := seedHitTestArtifact(t, db)
	db.BatchHits(time.Hour, discardLogger)

	for range 2 {
		_ = db.RecordArtifactHit(versionPURL, filename)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}

	db, err = Open(path)
	if err != nil {
		t.Fatalf("Open failed: %v", err)
	}
	defer func() { _ = db.Close() }()
	if got := hitCount(t, db, versionPURL, filename); got != 2 {
		t.Errorf("hit count after Close = %d, want 2", got)
	}
}

// TestBatchHitsKeepNewerAccessTime flushes a batch older than a hit already
// written, as when proxies share a database. The count still adds up, but
// neither timestamp may move backwards.
func TestBatchHitsKeepNewerAccessTime(t *testing.T) {
	runWithBothDatabases(t, func(t *testing.T, db *DB) {
		versionPURL, filename := seedHitTestArtifact(t, db)
		k := hitKey{versionPURL, filename}
		now := time.Now()
		read := func() *Artifact {
			t.Helper()
			a, err := db.GetArtifact(versionPURL, filename)
			if err != nil || a == nil {
				t.Fatalf("GetArtifact failed: %v", err)
			}
			return a
		}
		write := func(last time.Time) {
			t.Helper()
			if err := db.writeHits(map[hitKey]hitEntry{k: {count: 1, last: last}}); err != nil {
				t.Fatalf("writeHits failed: %v", err)
			}
		}

		write(now)
		newer := read()
		if !newer.LastAccessedAt.Valid {
			t.Fatal("last_accessed_at not set from NULL")
		}

		write(now.Add(-time.Minute))
		got := read()
		if got.HitCount != 2 {
			t.Errorf("hit count = %d, want 2", got.HitCount)
		}
		if !got.LastAccessedAt.Time.Equal(newer.LastAccessedAt.Time) {
			t.Errorf("last_accessed_at moved from %v to %v", newer.LastAccessedAt.Time, got.LastAccessedAt.Time)
		}
		if !got.UpdatedAt.Equal(newer.UpdatedAt) {
			t.Errorf("updated_at moved from %v to %v", newer.UpdatedAt, got.UpdatedAt)
		}

		write(now.Add(time.Minute))
		got = read()
		if !got.LastAccessedAt.Time.After(newer.LastAccessedAt.Time) || !got.UpdatedAt.After(newer.UpdatedAt) {
			t.Error("a newer batch did not advance the timestamps")
		}
	})
}

func TestBatchHitsZeroIntervalWritesImmediately(t *testing.T) {
	runWithBothDatabases(t, func(t *testing.T, db *DB) {
		versionPURL, filename := seedHitTestArtifact(t, db)
		db.BatchHits(0, discardLogger)

		_ = db.RecordArtifactHit(versionPURL, filename)
		if got := hitCount(t, db, versionPURL, filename); got != 1 {
			t.Errorf("hit count = %d, want 1", got)
		}
	})
}

func TestBatchHitsKeepsHitsWhenWriteFails(t *testing.T) {
	db := createTestDB(t)
	versionPURL, filename := seedHitTestArtifact(t, db)
	db.BatchHits(time.Hour, discardLogger)

	for range 2 {
		_ = db.RecordArtifactHit(versionPURL, filename)
	}
	_ = db.DB.Close()
	if err := db.flushHits(); err == nil {
		t.Fatal("expected flushHits to fail on a closed database")
	}
	if got := db.hits.take()[hitKey{versionPURL, filename}].count; got != 2 {
		t.Errorf("pending hits after failed flush = %d, want 2", got)
	}
	_ = db.Close()
}

// TestBatchHitsCountsEveryHitWhileFlushing records hits from several goroutines
// while another flushes, so no hit may be lost between taking the pending
// hits and writing them.
func TestBatchHitsCountsEveryHitWhileFlushing(t *testing.T) {
	runWithBothDatabases(t, func(t *testing.T, db *DB) {
		versionPURL, filename := seedHitTestArtifact(t, db)
		db.BatchHits(time.Hour, discardLogger)

		const writers, hitsEach = 8, 100
		stop := make(chan struct{})
		flushed := make(chan struct{})
		go func() {
			defer close(flushed)
			for {
				select {
				case <-stop:
					return
				default:
					if err := db.flushHits(); err != nil {
						t.Errorf("flushHits failed: %v", err)
						return
					}
				}
			}
		}()
		var wg sync.WaitGroup
		for range writers {
			wg.Go(func() {
				for range hitsEach {
					if err := db.RecordArtifactHit(versionPURL, filename); err != nil {
						t.Errorf("RecordArtifactHit failed: %v", err)
					}
				}
			})
		}
		wg.Wait()
		close(stop)
		<-flushed

		if err := db.flushHits(); err != nil {
			t.Fatalf("final flushHits failed: %v", err)
		}
		if got := hitCount(t, db, versionPURL, filename); got != writers*hitsEach {
			t.Errorf("hit count = %d, want %d", got, writers*hitsEach)
		}
	})
}

func TestBatchHitsLogsHitsDroppedOnClose(t *testing.T) {
	db := createTestDB(t)
	versionPURL, filename := seedHitTestArtifact(t, db)
	var logs bytes.Buffer
	db.BatchHits(time.Hour, slog.New(slog.NewTextHandler(&logs, nil)))

	_ = db.RecordArtifactHit(versionPURL, filename)
	_ = db.DB.Close()
	_ = db.Close()

	if !strings.Contains(logs.String(), "dropping them") {
		t.Errorf("no error logged for hits dropped on close, logs: %q", logs.String())
	}
}

func TestCloseTwiceWithBatchHits(t *testing.T) {
	db := createTestDB(t)
	db.BatchHits(time.Hour, discardLogger)
	_ = db.Close()
	_ = db.Close()
}
