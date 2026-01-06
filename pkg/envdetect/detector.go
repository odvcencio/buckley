package envdetect

import (
	"bufio"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Detector scans a directory and builds an environment profile
type Detector struct {
	rootPath string
	cache    *Cache
}

// NewDetector creates a new detector for the given root path
func NewDetector(rootPath string) *Detector {
	return &Detector{
		rootPath: rootPath,
		cache:    NewCache(filepath.Join(rootPath, ".buckley", "cache")),
	}
}

// Detect walks the repo and builds the profile
func (d *Detector) Detect() (*EnvironmentProfile, error) {
	// Check cache first
	cacheKey := d.computeCacheKey()
	if cached, ok := d.cache.Get(cacheKey); ok {
		return cached, nil
	}

	profile := &EnvironmentProfile{
		Languages:  []Language{},
		Services:   []Service{},
		Ports:      []Port{},
		Volumes:    []Volume{},
		EnvVars:    []string{},
		DetectedAt: time.Now(),
		CacheKey:   cacheKey,
	}

	// Detect languages
	if err := d.detectLanguages(profile); err != nil {
		return nil, err
	}

	// Detect services
	if err := d.detectServices(profile); err != nil {
		return nil, err
	}

	// Cache result
	if err := d.cache.Set(cacheKey, profile); err != nil {
		return nil, err
	}

	return profile, nil
}

// detectLanguages scans for language-specific files
func (d *Detector) detectLanguages(p *EnvironmentProfile) error {
	signatures := map[string]LanguageSignature{
		"go": {
			Lockfiles:    []string{"go.mod", "go.sum"},
			VersionFile:  "go.mod",
			VersionRegex: `go\s+(\d+\.\d+)`,
		},
		"node": {
			Lockfiles:   []string{"package.json", "package-lock.json"},
			VersionFile: ".nvmrc",
		},
		"rust": {
			Lockfiles:   []string{"Cargo.toml", "Cargo.lock"},
			VersionFile: "rust-toolchain.toml",
		},
		"python": {
			Lockfiles:   []string{"pyproject.toml", "requirements.txt", "Pipfile"},
			VersionFile: ".python-version",
		},
	}

	for name, sig := range signatures {
		if found := d.scanForSignature(sig); found {
			lang := Language{
				Name:       name,
				Version:    d.extractVersion(sig),
				Lockfiles:  d.findFiles(sig.Lockfiles),
				BuildTools: d.getBuildTools(name),
			}
			p.Languages = append(p.Languages, lang)
		}
	}

	return nil
}

// detectServices scans for service indicators
func (d *Detector) detectServices(p *EnvironmentProfile) error {
	// Check for docker-compose.yml
	composePath := filepath.Join(d.rootPath, "docker-compose.yml")
	if fileExists(composePath) {
		if services, err := d.parseComposeFile(composePath); err == nil {
			p.Services = append(p.Services, services...)
		}
	}

	// Scan for common client imports
	if hasPostgres := d.scanForImport("database/sql", "github.com/lib/pq", "github.com/jackc/pgx"); hasPostgres {
		p.Services = append(p.Services, Service{
			Type:    "postgres",
			Version: "16",
			Ports:   []Port{{Host: 5432, Container: 5432, Protocol: "tcp"}},
			Volumes: []Volume{{Name: "postgres_data", Path: "/var/lib/postgresql/data", Type: "named"}},
			Env: map[string]string{
				"POSTGRES_DB":       "buckley_dev",
				"POSTGRES_USER":     "buckley",
				"POSTGRES_PASSWORD": "dev_password",
			},
		})
	}

	if hasRedis := d.scanForImport("github.com/redis/go-redis", "github.com/go-redis/redis"); hasRedis {
		p.Services = append(p.Services, Service{
			Type:    "redis",
			Version: "7",
			Ports:   []Port{{Host: 6379, Container: 6379, Protocol: "tcp"}},
		})
	}

	if hasMongo := d.scanForImport("go.mongodb.org/mongo-driver"); hasMongo {
		p.Services = append(p.Services, Service{
			Type:    "mongodb",
			Version: "6",
			Ports:   []Port{{Host: 27017, Container: 27017, Protocol: "tcp"}},
			Volumes: []Volume{{Name: "mongodb_data", Path: "/data/db", Type: "named"}},
		})
	}

	return nil
}

// scanForSignature checks if any signature files exist
func (d *Detector) scanForSignature(sig LanguageSignature) bool {
	for _, file := range sig.Lockfiles {
		if fileExists(filepath.Join(d.rootPath, file)) {
			return true
		}
	}
	return false
}

// extractVersion extracts version from version file
func (d *Detector) extractVersion(sig LanguageSignature) string {
	if sig.VersionFile == "" {
		return "latest"
	}

	versionPath := filepath.Join(d.rootPath, sig.VersionFile)
	if !fileExists(versionPath) {
		return "latest"
	}

	data, err := os.ReadFile(versionPath)
	if err != nil {
		return "latest"
	}

	if sig.VersionRegex != "" {
		re := regexp.MustCompile(sig.VersionRegex)
		matches := re.FindStringSubmatch(string(data))
		if len(matches) > 1 {
			return matches[1]
		}
	}

	// For simple version files (like .nvmrc, .python-version)
	version := strings.TrimSpace(string(data))
	if version != "" {
		return version
	}

	return "latest"
}

// findFiles returns existing files from a list
func (d *Detector) findFiles(files []string) []string {
	found := []string{}
	for _, file := range files {
		if fileExists(filepath.Join(d.rootPath, file)) {
			found = append(found, file)
		}
	}
	return found
}

// getBuildTools returns build tools for a language
func (d *Detector) getBuildTools(lang string) []string {
	tools := map[string][]string{
		"go":     {"go"},
		"node":   {"npm", "yarn", "pnpm"},
		"rust":   {"cargo"},
		"python": {"pip", "poetry"},
	}
	return tools[lang]
}

// scanForImport checks if any of the given import patterns exist in Go files
func (d *Detector) scanForImport(imports ...string) bool {
	// Only scan if this is a Go project
	if !fileExists(filepath.Join(d.rootPath, "go.mod")) {
		return false
	}

	found := false
	filepath.Walk(d.rootPath, func(path string, info os.FileInfo, err error) error {
		if err != nil || found {
			return nil
		}

		// Skip vendor and hidden directories
		if info.IsDir() {
			name := info.Name()
			if name == "vendor" || name == ".git" || strings.HasPrefix(name, ".") {
				return filepath.SkipDir
			}
			return nil
		}

		// Only scan .go files
		if !strings.HasSuffix(path, ".go") {
			return nil
		}

		file, err := os.Open(path)
		if err != nil {
			return nil
		}
		defer file.Close()

		scanner := bufio.NewScanner(file)
		for scanner.Scan() {
			line := scanner.Text()
			for _, imp := range imports {
				if strings.Contains(line, imp) {
					found = true
					return nil
				}
			}
		}

		return nil
	})

	return found
}

// parseComposeFile parses an existing docker-compose.yml
func (d *Detector) parseComposeFile(path string) ([]Service, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var compose struct {
		Services map[string]struct {
			Image       string            `yaml:"image"`
			Ports       []string          `yaml:"ports"`
			Environment map[string]string `yaml:"environment"`
		} `yaml:"services"`
	}

	if err := yaml.Unmarshal(data, &compose); err != nil {
		return nil, err
	}

	services := []Service{}
	for name, svc := range compose.Services {
		// Extract service type from image name
		serviceType := strings.Split(svc.Image, ":")[0]
		version := "latest"
		if parts := strings.Split(svc.Image, ":"); len(parts) > 1 {
			version = parts[1]
		}

		services = append(services, Service{
			Type:    serviceType,
			Version: version,
			Env:     svc.Environment,
		})
		_ = name // Use name if needed later
	}

	return services, nil
}

// computeCacheKey generates a cache key from all lockfiles
func (d *Detector) computeCacheKey() string {
	lockfiles := []string{
		"go.mod", "go.sum",
		"package.json", "package-lock.json",
		"Cargo.toml", "Cargo.lock",
		"pyproject.toml", "requirements.txt", "Pipfile",
	}

	return computeCacheKey(d.rootPath, lockfiles)
}

// fileExists checks if a file exists
func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
