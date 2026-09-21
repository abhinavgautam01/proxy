package handler

func (p *Proxy) versionDenied(ecosystem, name, version string) bool {
	if p.Denylist == nil {
		return false
	}
	return p.Denylist.Denied(canonicalVersionPURL(ecosystem, name, version))
}
