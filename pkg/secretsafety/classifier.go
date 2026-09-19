package secretsafety

import (
	"path/filepath"
	"strings"
	"unicode"
	"unicode/utf8"
)

// UnsafePath reports whether a path contains bytes or Unicode formatting
// controls that make its displayed identity unsafe.
func UnsafePath(path string) bool {
	if !utf8.ValidString(path) {
		return true
	}
	for _, r := range path {
		if unicode.IsControl(r) || unicode.In(r, unicode.Cf) {
			return true
		}
	}
	return false
}

// SecretContent conservatively recognizes credential material in text.
func SecretContent(content []byte) bool {
	text := strings.ToLower(string(content))
	if hasArmoredPrivateKey(text) {
		return true
	}
	for _, token := range []struct {
		prefix string
		min    int
		exact  int
	}{
		{prefix: "github_pat_", min: 50},
		{prefix: "ghp_", exact: 36},
		{prefix: "xoxb-", min: 24},
		{prefix: "xoxp-", min: 24},
	} {
		if hasPrefixedSecret(text, token.prefix, token.min, token.exact) {
			return true
		}
	}
	for _, assignment := range []struct {
		name string
		min  int
	}{
		{name: "aws_secret_access_key", min: 40},
		{name: "authorization: bearer", min: 32},
		{name: "x-api-key:", min: 32},
		{name: "api_key=", min: 32},
		{name: "api-key=", min: 32},
		{name: "access_token=", min: 32},
		{name: "client_secret=", min: 32},
	} {
		if hasAssignedSecret(text, assignment.name, assignment.min) {
			return true
		}
	}
	return false
}

// AutomaticDisclosureSecretContent preserves the deliberately conservative
// substring boundary used before Buckley automatically captures an untracked
// file. Workspace admission uses SecretContent's value-shape checks so tracked
// source that documents these markers remains admissible.
func AutomaticDisclosureSecretContent(content []byte) bool {
	text := strings.ToLower(string(content))
	markers := []string{
		"aws_secret_access_key=",
		"aws_secret_access_key:",
		"github_pat_",
		"ghp_",
		"xoxb-",
		"xoxp-",
		"authorization: bearer ",
		"x-api-key:",
		"api_key=",
		"api-key=",
		"access_token=",
		"client_secret=",
	}
	for _, kind := range []string{"", "openssh ", "rsa ", "ec ", "dsa ", "encrypted "} {
		markers = append(markers, "-----begin "+kind+"private"+" key-----")
	}
	markers = append(markers, "-----begin pgp private"+" key block-----")
	for _, marker := range markers {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func hasArmoredPrivateKey(text string) bool {
	kinds := []string{"", "openssh ", "rsa ", "ec ", "dsa ", "encrypted "}
	for _, kind := range kinds {
		header := "-----begin " + kind + "private" + " key-----"
		if hasArmoredPayload(text, header) {
			return true
		}
	}
	return hasArmoredPayload(text, "-----begin pgp private"+" key block-----")
}

func hasArmoredPayload(text, header string) bool {
	for offset := 0; offset < len(text); {
		index := strings.Index(text[offset:], header)
		if index < 0 {
			return false
		}
		start := offset + index + len(header)
		if start < len(text) && text[start] == '\r' {
			start++
		}
		if start < len(text) && text[start] == '\n' {
			limit := start + 2048
			if limit > len(text) {
				limit = len(text)
			}
			for _, line := range strings.Split(text[start+1:limit], "\n") {
				line = strings.TrimSpace(line)
				if len(line) >= 32 && secretAlphabet(line) && secretDiversity(line) {
					return true
				}
			}
		}
		offset = start
	}
	return false
}

func hasPrefixedSecret(text, prefix string, minimum, exact int) bool {
	for offset := 0; offset < len(text); {
		index := strings.Index(text[offset:], prefix)
		if index < 0 {
			return false
		}
		start := offset + index + len(prefix)
		value := secretToken(text[start:])
		if (exact == 0 && len(value) >= minimum || exact > 0 && len(value) == exact) && secretDiversity(value) {
			return true
		}
		offset = start
	}
	return false
}

func hasAssignedSecret(text, marker string, minimum int) bool {
	for offset := 0; offset < len(text); {
		index := strings.Index(text[offset:], marker)
		if index < 0 {
			return false
		}
		start := offset + index + len(marker)
		if marker == "aws_secret_access_key" {
			for start < len(text) && (text[start] == ' ' || text[start] == '\t') {
				start++
			}
			if start >= len(text) || text[start] != '=' && text[start] != ':' {
				offset = start
				continue
			}
			start++
		}
		for start < len(text) && (text[start] == ' ' || text[start] == '\t' || text[start] == '\'' || text[start] == '"') {
			start++
		}
		value := secretToken(text[start:])
		if len(value) >= minimum && secretDiversity(value) {
			return true
		}
		offset = start
	}
	return false
}

func secretToken(value string) string {
	end := 0
	for end < len(value) {
		b := value[end]
		if !((b >= 'a' && b <= 'z') || (b >= '0' && b <= '9') || strings.ContainsRune("_./+=:-", rune(b))) {
			break
		}
		end++
	}
	return value[:end]
}

func secretAlphabet(value string) bool {
	for _, r := range value {
		if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || strings.ContainsRune("+/=", r)) {
			return false
		}
	}
	return true
}

func secretDiversity(value string) bool {
	var seen [256]bool
	distinct := 0
	for idx := 0; idx < len(value); idx++ {
		if !seen[value[idx]] {
			seen[value[idx]] = true
			distinct++
		}
	}
	return distinct >= 8
}

// BinaryContent reports data that cannot safely be treated as ordinary text.
func BinaryContent(content []byte) bool {
	if !utf8.Valid(content) {
		return true
	}
	for _, b := range content {
		if (b < 0x20 && b != '\n' && b != '\r' && b != '\t') || b == 0x7f {
			return true
		}
	}
	return false
}

// CredentialPath reports names that conservatively identify credential or
// secret material. Unlike SensitivePath it does not classify ordinary binary
// artifacts solely by extension.
func CredentialPath(path string) bool {
	lowerPath := strings.ToLower(filepath.ToSlash(path))
	base := strings.ToLower(filepath.Base(lowerPath))
	if UnsafePath(path) || secretDirectory(lowerPath) {
		return true
	}

	secretFiles := []string{
		".envrc",
		"credentials.json", "credentials.yaml", "credentials.yml",
		"secrets.json", "secrets.yaml", "secrets.yml",
		".secrets",
		"id_rsa", "id_ed25519", "id_ecdsa", "id_dsa",
		".pem", ".key", ".p12", ".pfx",
		".htpasswd", ".netrc", ".npmrc", ".pypirc", ".git-credentials",
		"service-account.json", "serviceaccount.json",
		"kubeconfig", ".kube/config",
	}
	for _, secret := range secretFiles {
		if base == secret || strings.HasSuffix(base, secret) {
			return true
		}
	}
	safeDotenvExample := base == "sample.env" || base == "example.env" || base == ".env.example"
	if !safeDotenvExample && (strings.HasPrefix(base, ".env") || strings.HasSuffix(base, ".env") || strings.Contains(base, ".env.")) {
		return true
	}
	if sensitiveDataName(base) {
		return true
	}
	if lowerPath == ".kube/config" || strings.HasSuffix(lowerPath, "/.kube/config") {
		return true
	}
	if lowerPath == ".docker/config.json" || strings.HasSuffix(lowerPath, "/.docker/config.json") {
		return true
	}
	return strings.Contains(lowerPath, ".aws/") && (base == "credentials" || base == "config")
}

// SensitivePath applies Buckley's conservative automatic-disclosure boundary.
// Binary artifacts remain excluded from review/disclosure even when their
// names do not imply credentials.
func SensitivePath(path string) bool {
	return CredentialPath(path) || BinaryPath(path)
}

// BinaryPath reports file types excluded from automatic source disclosure.
func BinaryPath(base string) bool {
	base = strings.ToLower(filepath.Base(filepath.ToSlash(base)))
	for _, extension := range []string{
		".zip", ".tar", ".gz", ".bz2", ".7z", ".rar",
		".mp4", ".mov", ".avi", ".mkv", ".webm",
		".mp3", ".wav", ".flac", ".aac",
		".png", ".jpg", ".jpeg", ".gif", ".webp", ".ico",
		".woff", ".woff2", ".ttf", ".otf",
		".psd", ".ai", ".sketch",
		".sqlite", ".sqlite3", ".db", ".wasm",
		".pdf", ".doc", ".docx", ".xls", ".xlsx", ".ppt", ".pptx",
		".bin", ".o", ".a", ".so", ".dylib", ".dll", ".exe", ".class",
		".jar", ".war", ".apk",
	} {
		if strings.HasSuffix(base, extension) {
			return true
		}
	}
	return false
}

func secretDirectory(path string) bool {
	parts := strings.Split(filepath.ToSlash(path), "/")
	for _, part := range parts[:len(parts)-1] {
		switch part {
		case "secret", "secrets", ".secrets", "credential", "credentials", ".credentials", ".aws", ".kube", ".ssh":
			return true
		}
	}
	return false
}

func sensitiveDataName(base string) bool {
	if !hasSecretDataSuffix(base) {
		return false
	}
	for _, marker := range []string{
		"secret", "credential", "password", "passwd", "token",
		"api-key", "api_key", "apikey", "private-key", "private_key",
		"service-account", "service_account", "serviceaccount", "oauth",
	} {
		if strings.Contains(base, marker) {
			return true
		}
	}
	return false
}

func hasSecretDataSuffix(base string) bool {
	for _, extension := range []string{
		".json", ".yaml", ".yml", ".txt", ".ini", ".conf", ".cfg",
		".toml", ".properties", ".xml", ".csv",
	} {
		if strings.HasSuffix(base, extension) {
			return true
		}
	}
	return base == "secret" || base == "secrets" || base == "credential" || base == "credentials"
}
