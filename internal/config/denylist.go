package config

// DenylistConfig identifies exact package versions that must not be served.
type DenylistConfig struct {
	Packages []string `json:"packages" yaml:"packages"`
}
