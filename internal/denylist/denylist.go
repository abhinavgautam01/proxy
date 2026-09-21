// Package denylist implements an immutable, exact-version package policy.
package denylist

import (
	"fmt"
	"strings"

	"github.com/git-pkgs/purl"
)

// Policy is safe for concurrent reads. A nil policy allows every version.
type Policy struct {
	packages map[string]map[string]bool
}

// New validates and canonicalizes versioned PURLs. Qualifiers and subpaths are
// rejected because the proxy cannot reliably distinguish them at every endpoint.
func New(packages []string) (*Policy, error) {
	if len(packages) == 0 {
		return nil, nil
	}
	p := &Policy{packages: make(map[string]map[string]bool)}
	for _, value := range packages {
		pkg, err := purl.Parse(value)
		if err != nil {
			return nil, fmt.Errorf("invalid denylist package %q: %w", value, err)
		}
		if pkg.Version == "" || len(pkg.Qualifiers) != 0 || pkg.Subpath != "" ||
			strings.ContainsAny(pkg.Namespace+pkg.Name+pkg.Version, "*?[]<>=| \t\r\n") {
			return nil, fmt.Errorf("invalid denylist package %q: expected an exact versioned PURL without qualifiers or subpath", value)
		}
		key := pkg.WithoutVersion().String()
		if p.packages[key] == nil {
			p.packages[key] = make(map[string]bool)
		}
		p.packages[key][pkg.Version] = true
	}
	return p, nil
}

// Denied reports whether a versioned PURL is denied.
func (p *Policy) Denied(value string) bool {
	if p == nil || len(p.packages) == 0 {
		return false
	}
	pkg, err := purl.Parse(value)
	if err != nil {
		return false
	}
	return p.packages[pkg.WithoutVersion().String()][pkg.Version]
}

// Versions returns a caller-owned set of denied versions for a package PURL.
func (p *Policy) Versions(packagePURL string) map[string]bool {
	if p == nil {
		return nil
	}
	versions := p.packages[packagePURL]
	if len(versions) == 0 {
		return nil
	}
	result := make(map[string]bool, len(versions))
	for version := range versions {
		result[version] = true
	}
	return result
}
