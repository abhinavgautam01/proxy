package handler

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/git-pkgs/registries"
	"github.com/git-pkgs/registries/client"
	"github.com/git-pkgs/registries/fetch"
)

type artifactTestRegistry struct {
	calls     atomic.Int64
	err       error
	artifacts []registries.Artifact
}

func (*artifactTestRegistry) Ecosystem() string { return "maven" }
func (*artifactTestRegistry) URLs() client.URLBuilder { //nolint:ireturn // required by fetch.Registry
	return &client.BaseURLs{}
}
func (r *artifactTestRegistry) FetchVersions(ctx context.Context, _ string) ([]registries.Version, error) {
	r.calls.Add(1)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if r.err != nil {
		return nil, r.err
	}
	return []registries.Version{{Number: "1.0.0", Artifacts: r.artifacts}}, nil
}

func TestGetOrFetchArtifactResolvedCacheHit(t *testing.T) {
	p, db, store, fetcher := setupTestProxy(t)
	fetcher.fetchErr = errors.New("unexpected artifact fetch on a cache hit")
	seedPackage(t, db, store, "maven", "org.example:demo", "1.0.0", "demo-1.0.0.pom", "pom")
	seedPackage(t, db, store, "maven", "org.example:demo", "1.0.0", "demo-1.0.0.jar", "jar")
	registry := &artifactTestRegistry{artifacts: []registries.Artifact{{URL: "https://registry.example/demo-1.0.0.jar", Filename: "demo-1.0.0.jar"}}}
	p.Resolver.RegisterRegistry(registry)
	result, err := p.GetOrFetchArtifact(context.Background(), "maven", "org.example:demo", "1.0.0", "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = result.Reader.Close() }()
	body, err := io.ReadAll(result.Reader)
	if err != nil || string(body) != "jar" || !result.Cached || result.Artifact.Filename != "demo-1.0.0.jar" {
		t.Fatalf("result=%+v body=%q err=%v", result, body, err)
	}
	if fetcher.fetchCalled || registry.calls.Load() != 1 {
		t.Fatalf("fetchCalled=%t resolutions=%d", fetcher.fetchCalled, registry.calls.Load())
	}
}

func TestGetOrFetchArtifactResolvedMiss(t *testing.T) {
	p, db, store, _ := setupTestProxy(t)
	seedPackage(t, db, store, "maven", "org.example:demo", "1.0.0", "demo-1.0.0.pom", "pom")
	registry := &artifactTestRegistry{artifacts: []registries.Artifact{{URL: "https://registry.example/demo-1.0.0.jar", Filename: "demo-1.0.0.jar"}}}
	p.Resolver.RegisterRegistry(registry)
	fetcher := &countingFetcher{content: "jar"}
	p.Fetcher = fetcher
	for i, cached := range []bool{false, true} {
		result, err := p.GetOrFetchArtifact(context.Background(), "maven", "org.example:demo", "1.0.0", "")
		if err != nil {
			t.Fatal(err)
		}
		body, readErr := io.ReadAll(result.Reader)
		_ = result.Reader.Close()
		if readErr != nil || string(body) != "jar" || result.Cached != cached {
			t.Fatalf("result=%+v body=%q err=%v; want cached=%t", result, body, readErr, cached)
		}
		if registry.calls.Load() != int64(i+1) {
			t.Fatalf("resolutions=%d after request %d; want one per request", registry.calls.Load(), i+1)
		}
	}
	if fetcher.calls.Load() != 1 || registry.calls.Load() != 2 {
		t.Fatalf("downloads=%d resolutions=%d; want 1 and 2", fetcher.calls.Load(), registry.calls.Load())
	}
}

func TestGetOrFetchArtifactNamedCacheHitDoesNotResolve(t *testing.T) {
	p, db, store, _ := setupTestProxy(t)
	seedPackage(t, db, store, "maven", "org.example:demo", "1.0.0", "demo-1.0.0.pom", "pom")
	p.Resolver = nil // A named cache hit must not need a resolver.
	result, err := p.GetOrFetchArtifact(context.Background(), "maven", "org.example:demo", "1.0.0", "demo-1.0.0.pom")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = result.Reader.Close() }()
	if !result.Cached {
		t.Fatal("expected cache hit")
	}
}

func TestGetOrFetchArtifactResolvedErrors(t *testing.T) {
	for _, want := range []error{fetch.ErrNotFound, context.Canceled, errors.New("registry unavailable")} {
		t.Run(want.Error(), func(t *testing.T) {
			p, _, _, fetcher := setupTestProxy(t)
			registry := &artifactTestRegistry{err: want}
			p.Resolver.RegisterRegistry(registry)
			_, err := p.GetOrFetchArtifact(context.Background(), "maven", "org.example:demo", "1.0.0", "")
			if !errors.Is(err, want) || fetcher.fetchCalled {
				t.Fatalf("err=%v fetched=%t", err, fetcher.fetchCalled)
			}
			if errors.Is(want, fetch.ErrNotFound) && !errors.Is(err, ErrUpstreamNotFound) {
				t.Fatalf("missing upstream not-found classification: %v", err)
			}
		})
	}
}

func TestGetOrFetchArtifactResolvedMissingStorage(t *testing.T) {
	p, db, store, fetcher := setupTestProxy(t)
	seedPackage(t, db, store, "npm", "demo", "1.0.0", "demo-1.0.0.tgz", "old")
	clear(store.files)
	fetcher.artifact = &fetch.Artifact{Body: io.NopCloser(strings.NewReader("new"))}
	result, err := p.GetOrFetchArtifact(context.Background(), "npm", "demo", "1.0.0", "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = result.Reader.Close() }()
	if result.Cached || !fetcher.fetchCalled {
		t.Fatalf("expected refetch: result=%+v fetched=%t", result, fetcher.fetchCalled)
	}
}

func TestGetOrFetchArtifactResolvedDenyBeforeResolution(t *testing.T) {
	p, _, _, fetcher := setupTestProxy(t)
	registry := &artifactTestRegistry{err: errors.New("must not resolve")}
	p.Resolver.RegisterRegistry(registry)
	setTestDenylist(t, p, "pkg:maven/org.example/demo@1.0.0")
	_, err := p.GetOrFetchArtifact(context.Background(), "maven", "org.example:demo", "1.0.0", "")
	if !errors.Is(err, ErrVersionDenied) || registry.calls.Load() != 0 || fetcher.fetchCalled {
		t.Fatalf("err=%v resolutions=%d fetched=%t", err, registry.calls.Load(), fetcher.fetchCalled)
	}
}

func TestGetOrFetchArtifactResolvedConcurrentMisses(t *testing.T) {
	p, _, _, _ := setupTestProxy(t)
	fetcher := &countingFetcher{content: "tarball", delay: fetchHoldTime}
	p.Fetcher = fetcher
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			<-start
			result, err := p.GetOrFetchArtifact(context.Background(), "npm", "demo", "1.0.0", "")
			if err != nil {
				t.Error(err)
				return
			}
			defer func() { _ = result.Reader.Close() }()
			body, err := io.ReadAll(result.Reader)
			if err != nil || string(body) != "tarball" {
				t.Errorf("body=%q err=%v", body, err)
			}
		})
	}
	close(start)
	wg.Wait()
	if fetcher.calls.Load() != 1 {
		t.Fatalf("downloads=%d, want 1", fetcher.calls.Load())
	}
}

func TestGetOrFetchArtifactResolvedWithoutFilename(t *testing.T) {
	p, _, _, fetcher := setupTestProxy(t)
	registry := &artifactTestRegistry{artifacts: []registries.Artifact{{URL: "https://registry.example/"}}}
	p.Resolver.RegisterRegistry(registry)
	_, err := p.GetOrFetchArtifact(context.Background(), "maven", "org.example:demo", "1.0.0", "")
	if err == nil || !strings.Contains(err.Error(), "no filename") || fetcher.fetchCalled {
		t.Fatalf("err=%v fetched=%t; expected rejection before download", err, fetcher.fetchCalled)
	}
}

func TestGetOrFetchArtifactResolvedInvalidCacheHash(t *testing.T) {
	p, db, store, fetcher := setupTestProxy(t)
	seedPackage(t, db, store, "npm", "demo", "1.0.0", "demo-1.0.0.tgz", "old")
	art, err := db.GetArtifact("pkg:npm/demo@1.0.0", "demo-1.0.0.tgz")
	if err != nil {
		t.Fatal(err)
	}
	art.ContentHash.String = "invalid-hash"
	if err := db.UpsertArtifact(art); err != nil {
		t.Fatal(err)
	}
	fetcher.artifact = &fetch.Artifact{Body: io.NopCloser(strings.NewReader("new"))}
	result, err := p.GetOrFetchArtifact(context.Background(), "npm", "demo", "1.0.0", "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = result.Reader.Close() }()
	body, err := io.ReadAll(result.Reader)
	if err != nil || string(body) != "new" || result.Cached || !fetcher.fetchCalled {
		t.Fatalf("result=%+v body=%q err=%v fetched=%t", result, body, err, fetcher.fetchCalled)
	}
}
