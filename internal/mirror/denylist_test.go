package mirror

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/git-pkgs/proxy/internal/denylist"
	"github.com/git-pkgs/proxy/internal/handler"
)

func TestMirrorOneDenylist(t *testing.T) {
	policy, err := denylist.New([]string{"pkg:npm/demo@1.0.0"})
	if err != nil {
		t.Fatal(err)
	}
	// No database, storage, or fetcher is needed: the denial comes first.
	p := &handler.Proxy{Denylist: policy}
	m := New(p, nil, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), 1)
	tracker := newProgressTracker()
	m.mirrorOne(context.Background(), PackageVersion{Ecosystem: "npm", Name: "demo", Version: "1.0.0"}, tracker)
	progress := tracker.snapshot()
	if progress.Failed != 1 || progress.Completed != 0 || progress.Skipped != 0 || len(progress.Errors) != 1 {
		t.Fatalf("progress = %+v", progress)
	}
	if !strings.Contains(progress.Errors[0].Error, "denylist") {
		t.Fatalf("unexpected error: %s", progress.Errors[0].Error)
	}
}
