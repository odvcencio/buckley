package envdetect

import "time"

// EnvironmentProfile represents detected project characteristics
type EnvironmentProfile struct {
	Languages  []Language
	Services   []Service
	Ports      []Port
	Volumes    []Volume
	EnvVars    []string
	DetectedAt time.Time
	CacheKey   string // Hash of lockfiles
}

// Language represents a detected programming language/runtime
type Language struct {
	Name       string   // "go", "node", "rust", "python"
	Version    string   // "1.22", "20", "1.75", "3.11"
	Lockfiles  []string // Paths to lockfiles
	BuildTools []string // "go", "npm", "cargo", "pip"
}

// Service represents a backing service (database, cache, etc.)
type Service struct {
	Type    string // "postgres", "redis", "mongodb"
	Version string // "16", "7", "6"
	Ports   []Port
	Env     map[string]string
	Volumes []Volume
}

// Port represents a port mapping
type Port struct {
	Host      int
	Container int
	Protocol  string // "tcp", "udp"
}

// Volume represents a volume mount
type Volume struct {
	Name string
	Path string
	Type string // "named", "bind"
}

// LanguageSignature defines patterns to detect a language
type LanguageSignature struct {
	Lockfiles    []string
	VersionFile  string
	VersionRegex string
}
