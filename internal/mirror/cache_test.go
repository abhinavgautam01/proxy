package mirror

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/git-pkgs/registries"
	"github.com/git-pkgs/registries/client"
	"github.com/git-pkgs/registries/fetch"
)

type mirrorCacheRegistry struct {
	downloadURL string
	resolutions atomic.Int64
}

type rejectingMirrorTransport struct{}

func (rejectingMirrorTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("unexpected artifact download on a cache hit")
}

func (*mirrorCacheRegistry) Ecosystem() string { return "npm" }
func (*mirrorCacheRegistry) FetchVersions(context.Context, string) ([]registries.Version, error) {
	return nil, errors.New("unexpected metadata request")
}
func (r *mirrorCacheRegistry) URLs() client.URLBuilder { //nolint:ireturn // required by fetch.Registry
	return &client.BaseURLs{DownloadFn: func(_, _ string) string {
		r.resolutions.Add(1)
		return r.downloadURL
	}}
}

func TestMirrorRepeatedRunSkipsCachedArtifact(t *testing.T) {
	var downloads atomic.Int64
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/example-1.0.0.tgz" {
			t.Errorf("unexpected artifact path: %s", r.URL.Path)
			http.NotFound(w, r)
			return
		}
		downloads.Add(1)
		_, _ = io.WriteString(w, "tarball bytes")
	}))
	defer upstream.Close()
	m := setupTestMirror(t, 1)
	registry := &mirrorCacheRegistry{downloadURL: upstream.URL + "/example-1.0.0.tgz"}
	m.proxy.Resolver.RegisterRegistry(registry)
	fetcher := fetch.NewFetcher(fetch.WithHTTPClient(upstream.Client()), fetch.WithMaxRetries(0))
	t.Cleanup(func() { _ = fetcher.Close() })
	m.proxy.Fetcher = fetcher
	source := &PURLSource{PURLs: []string{"pkg:npm/example@1.0.0"}}
	first, err := m.Run(context.Background(), source)
	if err != nil || first.Completed != 1 || first.Skipped != 0 || first.Failed != 0 {
		t.Fatalf("first mirror: progress=%+v err=%v", first, err)
	}
	before, err := m.db.GetArtifact("pkg:npm/example@1.0.0", "example-1.0.0.tgz")
	if err != nil {
		t.Fatal(err)
	}
	// The second run must succeed even when the artifact server is offline.
	upstream.Close()
	second, err := m.Run(context.Background(), source)
	if err != nil || second.Completed != 0 || second.Skipped != 1 || second.Failed != 0 {
		t.Fatalf("second mirror: progress=%+v err=%v", second, err)
	}
	after, err := m.db.GetArtifact("pkg:npm/example@1.0.0", "example-1.0.0.tgz")
	if err != nil {
		t.Fatal(err)
	}
	if before.FetchedAt != after.FetchedAt || before.ContentHash != after.ContentHash || before.StoragePath != after.StoragePath {
		t.Fatal("second mirror changed the cached artifact")
	}
	if downloads.Load() != 1 || registry.resolutions.Load() != 2 {
		t.Fatalf("downloads=%d resolutions=%d; want 1 and 2", downloads.Load(), registry.resolutions.Load())
	}
}
