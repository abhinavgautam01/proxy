package handler

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

const (
	debianUpstream = "http://deb.debian.org/debian"
	debMatchCount  = 4 // full match + name + version + arch
)

// DebianHandler handles APT/Debian repository protocol requests.
// It proxies requests to upstream Debian/Ubuntu repositories and caches .deb packages.
//
// The upstream given at /debian/ is the main archive. Additional archives are
// mounted at /debian/{repository}/, which is what makes a complete suite set
// reachable: security updates for a Debian release are served by a different
// archive than the main one, so one upstream URL cannot cover both.
type DebianHandler struct {
	proxy        *Proxy
	upstreamURL  string
	proxyURL     string
	repositories map[string]string
}

// NewDebianHandler creates a new Debian/APT protocol handler.
//
// upstreamURL is the main archive, served at /debian/. repositories maps names
// to additional archives, each served at /debian/{name}/; it may be empty, in
// which case only the main archive is reachable. A repository name shadows the
// main archive's root path of the same name, so config validation reserves
// "pool" and "dists" (see UpstreamConfig.Validate).
func NewDebianHandler(
	proxy *Proxy,
	proxyURL string,
	upstreamURL string,
	repositories map[string]string,
) *DebianHandler {
	if upstreamURL == "" {
		upstreamURL = debianUpstream
	}
	h := &DebianHandler{
		proxy:        proxy,
		upstreamURL:  strings.TrimSuffix(upstreamURL, "/"),
		proxyURL:     strings.TrimSuffix(proxyURL, "/"),
		repositories: make(map[string]string, len(repositories)),
	}
	for name, repositoryURL := range repositories {
		h.repositories[name] = strings.TrimSuffix(repositoryURL, "/")
	}
	return h
}

// Routes returns the HTTP handler for Debian requests.
// Mount this at /debian on your router.
func (h *DebianHandler) Routes() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		path := strings.TrimPrefix(r.URL.Path, "/")

		if containsPathTraversal(path) {
			http.Error(w, "invalid path", http.StatusBadRequest)
			return
		}

		// The main archive's own prefixes are matched first, so an empty
		// repository map leaves behaviour exactly as it was before named
		// repositories existed.
		//
		// An unconfigured first segment is not an error here, unlike the APK
		// handler's 404: the main archive is unnamed and serves paths of its
		// own at the root (README, indices/, project/), so an unknown name
		// stays a main-archive path. This is deliberate, not an oversight.
		upstreamURL, repository, rest := h.upstreamURL, "", path
		if !strings.HasPrefix(path, "pool/") && !strings.HasPrefix(path, "dists/") {
			if name, tail, ok := strings.Cut(path, "/"); ok && tail != "" {
				if named, found := h.repositories[name]; found {
					upstreamURL, repository, rest = named, name, tail
				}
			}
		}

		// Route based on path type
		switch {
		case strings.HasPrefix(rest, "pool/"):
			// Package downloads - cache these
			h.handlePackageDownload(w, r, repository, upstreamURL, rest)
		case strings.HasPrefix(rest, "dists/"):
			// Repository metadata - served through the metadata cache
			h.handleMetadata(w, r, repository, upstreamURL, rest)
		default:
			// Other files (like README, etc.) - proxy directly
			h.proxyFile(w, r, upstreamURL, rest)
		}
	})
}

// handlePackageDownload fetches and caches .deb packages from the pool.
// Pool path format: pool/{component}/{prefix}/{name}/{filename}
// Example: pool/main/n/nginx/nginx_1.18.0-6_amd64.deb
func (h *DebianHandler) handlePackageDownload(
	w http.ResponseWriter,
	r *http.Request,
	repository, upstreamURL, path string,
) {
	// Parse the path to extract package info
	name, version, arch := h.parsePoolPath(path)
	if name == "" {
		// Can't parse, just proxy directly
		h.proxyFile(w, r, upstreamURL, path)
		return
	}

	filename := path[strings.LastIndex(path, "/")+1:]
	downloadURL := fmt.Sprintf("%s/%s", upstreamURL, path)

	cacheFilename := h.artifactCacheFilename(repository, filename)

	h.proxy.Logger.Info("debian package download",
		"repository", repository,
		"name", name, "version", version, "arch", arch, "filename", filename)

	result, err := h.proxy.GetOrFetchArtifactFromURL(
		r.Context(), "deb", name, version, cacheFilename, downloadURL)
	if err != nil {
		h.proxy.serveArtifactError(w, err, "failed to fetch package")
		return
	}

	w.Header().Set(headerContentType, "application/vnd.debian.binary-package")
	ServeArtifact(w, result)
}

// handleMetadata serves repository metadata files through the metadata cache,
// so they keep resolving while the upstream archive is unreachable.
func (h *DebianHandler) handleMetadata(
	w http.ResponseWriter,
	r *http.Request,
	repository, upstreamURL, path string,
) {
	cacheKey := h.metadataCacheKeyFor(repository, upstreamURL, path)
	h.proxy.ProxyCached(w, r, fmt.Sprintf("%s/%s", upstreamURL, path), "debian", cacheKey, "*/*")
}

// artifactCacheFilename returns the artifact cache filename for a package.
//
// The same filename can hold different bytes in different archives, so a named
// repository scopes the entry by name. The main archive keeps the unscoped
// filename its existing cache entries were stored under.
func (h *DebianHandler) artifactCacheFilename(repository, filename string) string {
	if repository == "" {
		return filename
	}
	return repository + "/" + filename
}

// metadataCacheKeyFor returns the metadata cache key for a request.
//
// The main archive keeps its separator-based key so existing cache entries
// stay valid. A named repository hashes its name, its upstream URL, and the
// path instead: that keeps distinct repositories from sharing entries
// (repository names may contain '_', which the separator-based key would
// render ambiguous) and drops cached entries when a repository is repointed at
// a different upstream, mirroring APKHandler.metadataCacheKey.
func (h *DebianHandler) metadataCacheKeyFor(repository, upstreamURL, path string) string {
	if repository == "" {
		return strings.ReplaceAll(path, "/", "_")
	}
	identity := repository + "\x00" + upstreamURL + "\x00" + path
	digest := sha256.Sum256([]byte(identity))
	return hex.EncodeToString(digest[:])
}

// proxyFile proxies any file directly without caching.
func (h *DebianHandler) proxyFile(w http.ResponseWriter, r *http.Request, upstreamURL, path string) {
	h.proxy.ProxyFile(w, r, fmt.Sprintf("%s/%s", upstreamURL, path))
}

// debPackagePattern matches .deb filenames to extract name, version, and arch.
// Format: {name}_{version}_{arch}.deb
var debPackagePattern = regexp.MustCompile(`^(.+)_([^_]+)_([^_]+)\.deb$`)

// parsePoolPath extracts package info from a pool path.
// Example: pool/main/n/nginx/nginx_1.18.0-6_amd64.deb
func (h *DebianHandler) parsePoolPath(path string) (name, version, arch string) {
	// Get the filename
	idx := strings.LastIndex(path, "/")
	if idx < 0 {
		return "", "", ""
	}
	filename := path[idx+1:]

	// Parse the filename
	matches := debPackagePattern.FindStringSubmatch(filename)
	if len(matches) != debMatchCount {
		return "", "", ""
	}

	return matches[1], matches[2], matches[3]
}
