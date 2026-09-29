package commitmsg

import (
	"bufio"
	_ "embed"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// MinLeakTokenLen is the shortest token the removed-line check considers.
const MinLeakTokenLen = 4

// Rule names reported in Finding.Rule.
const (
	RuleRemovedEcho = "removed_echo"
	RuleDenyList    = "deny_list"
	RuleEmail       = "email"
	RuleIP          = "ip_address"
	RuleHost        = "internal_host"
	RuleSecret      = "secret_pattern"
)

// DefaultInternalHostPattern matches hostnames under common private suffixes.
// Override it with BUCKLEY_INTERNAL_HOST_PATTERN or ~/.buckley/internal-host-pattern.
const DefaultInternalHostPattern = `(?i)\b[a-z0-9][a-z0-9-]*(?:\.[a-z0-9-]+)*\.(?:internal|local|localdomain|corp|lan|intranet|home\.arpa|svc\.cluster\.local)\b`

// DefaultHostRegexp returns the compiled default internal-host pattern.
func DefaultHostRegexp() *regexp.Regexp { return regexp.MustCompile(DefaultInternalHostPattern) }

// Finding is one policy violation. Detail never contains the offending text,
// so it is safe to log and to send back to a model.
type Finding struct {
	Rule   string
	Detail string
	Count  int
}

func (f Finding) String() string { return f.Detail }

// Policy carries the inputs of the context-aware leak check.
type Policy struct {
	// DenyTerms come from private files and must never be logged or echoed.
	DenyTerms []string
	// HostPattern matches internal hostnames. Nil disables the host check.
	HostPattern *regexp.Regexp
}

// LeakError reports leak-check failures. It implements the sensitive marker
// used by the oneshot framework so the failing arguments are not echoed back.
type LeakError struct {
	Findings []Finding
}

func (e *LeakError) Error() string {
	parts := make([]string, 0, len(e.Findings))
	for _, f := range e.Findings {
		parts = append(parts, f.Detail)
	}
	return "message failed the safety check: " + strings.Join(parts, "; ") +
		". Rewrite it to describe intent and effect only; say \"the old name\" instead of naming removed or renamed identifiers, people, or organizations"
}

// SensitiveValidation tells the framework not to echo the rejected arguments.
func (e *LeakError) SensitiveValidation() bool { return true }

var stopwords = func() map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(`
	that this with from have will been were they them then than their there these those what when where which while
	would could should about after again also because before being between both cannot does doing done down each else
	even ever every into just like made make many more most much must never only other over same some such take
	them very want well your true false null none nil void return returns func function class type types const
	var let self string bool int error errors value values name names data file files line lines text test tests
	todo note notes info debug warn default defaults option options config configs list item items code case
	else elif import package export module public private static struct interface object objects field fields
	print println fmt json html http https path paths temp tmp result results state states event events
	handle handler handlers request response call calls used uses using use add adds added remove removes removed
	update updates updated fix fixes fixed change changes changed move moves moved rename renames renamed`) {
		m[w] = true
	}
	return m
}()

// commonWords lists ordinary English and technical words. A plain word that
// appears only on removed lines ("mixed", "discuss") is prose, not a name, so
// the removed-line check skips it. Names the model should never repeat belong
// in the private deny-list, which this list never overrides.
//
//go:embed commonwords.txt
var commonWordsText string

var commonWords = func() map[string]bool {
	m := map[string]bool{}
	for _, w := range strings.Fields(commonWordsText) {
		m[w] = true
	}
	return m
}()

// identifierShaped reports whether id carries structure that prose lacks:
// a separator, a digit, or a case change inside the word.
func identifierShaped(id string) bool {
	if strings.ContainsAny(id, "_-") {
		return true
	}
	seenLower := false
	for i, r := range id {
		switch {
		case unicode.IsDigit(r):
			return true
		case unicode.IsLower(r):
			seenLower = true
		case unicode.IsUpper(r) && i > 0 && seenLower:
			return true
		}
	}
	return false
}

var identRe = regexp.MustCompile(`[A-Za-z0-9_][A-Za-z0-9_-]*`)

// normalizeToken lowercases and strips separators so foo_bar, fooBar, and
// foo-bar compare equal.
func normalizeToken(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r == '_' || r == '-' || r == '.' {
			continue
		}
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

// splitWords splits an identifier at separators and camelCase boundaries.
func splitWords(id string) []string {
	var words []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			words = append(words, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
	}
	runes := []rune(id)
	for i, r := range runes {
		switch {
		case r == '_' || r == '-':
			flush()
			continue
		case unicode.IsUpper(r):
			if len(cur) > 0 {
				prev := runes[i-1]
				nextLower := i+1 < len(runes) && unicode.IsLower(runes[i+1])
				if unicode.IsLower(prev) || unicode.IsDigit(prev) || (unicode.IsUpper(prev) && nextLower) {
					flush()
				}
			}
		}
		cur = append(cur, r)
	}
	flush()
	return words
}

func considerable(norm string) bool {
	if len(norm) < MinLeakTokenLen || stopwords[norm] {
		return false
	}
	allDigits := true
	for _, r := range norm {
		if !unicode.IsDigit(r) {
			allDigits = false
			break
		}
	}
	return !allDigits
}

type diffTokens struct {
	removed, added, header map[string]bool // normalized identifiers
	removedW, addedW       map[string]bool // normalized words
	// sensitiveRemoved and sensitiveRemovedW hold only the removed tokens
	// classified as sensitive values: configuration values, /home path
	// segments, domain labels, deny-list names, and secret-shaped strings.
	// Ordinary code identifiers never enter these sets.
	sensitiveRemoved, sensitiveRemovedW map[string]bool
}

// minSensitiveValueLen is the length at which an unstructured word in a
// removed configuration value carries enough identity to be a name (shorter
// words like "prod" or "api" are environment labels, not secrets).
const minSensitiveValueLen = 7

// maxDataValueLen bounds the assignment values inspected as data. A longer
// value is prose (documentation text folded into a config field), whose words
// are not identifiers.
const maxDataValueLen = 80

var (
	dataValueRe = regexp.MustCompile(`^\s*[^:=\n]{1,120}?\s*[:=]\s*(\S.*)$`)
	homePathRe  = regexp.MustCompile(`/home/[A-Za-z0-9._-]+(?:/[^\s,;:'"()\]{}]*)*`)
	domainRe    = regexp.MustCompile(`(?i)\b[a-z0-9](?:[a-z0-9-]*[a-z0-9])?(?:\.[a-z0-9](?:[a-z0-9-]*[a-z0-9])?)*\.(?:com|net|org|io|dev|app|co|us|uk|eu|cn|jp|ai|sh|me|info|biz|cloud|tech|xyz|internal|local|localdomain|corp|lan|intranet)(?:\.[a-z]{2,3})?\b`)
)

// dataFileExts are the extensions whose lines hold configuration data. Values
// assigned there (namespaces, deployment names, hosts) are data the repository
// carried, not code the author named, so removed values are sensitive.
var dataFileExts = map[string]bool{
	".yaml": true, ".yml": true, ".json": true, ".toml": true,
	".ini": true, ".env": true, ".cfg": true, ".conf": true,
	".properties": true, ".tf": true, ".tfvars": true, ".csv": true,
	".xml": true,
}

func isDataFilePath(path string) bool {
	return dataFileExts[strings.ToLower(filepath.Ext(path))]
}

// diffPathLine extracts the file path from a --- or +++ line.
func diffPathLine(line string) string {
	path := strings.TrimSpace(line[3:])
	if path == "/dev/null" {
		return ""
	}
	if p, ok := strings.CutPrefix(path, "a/"); ok {
		return p
	}
	if p, ok := strings.CutPrefix(path, "b/"); ok {
		return p
	}
	return path
}

// diffGitHeaderPath extracts the b-side path from a `diff --git a/x b/y` line.
func diffGitHeaderPath(line string) string {
	fields := strings.Fields(line)
	if len(fields) < 4 {
		return ""
	}
	if p, ok := strings.CutPrefix(fields[len(fields)-1], "b/"); ok {
		return p
	}
	return ""
}

// secretShaped reports whether a raw identifier looks like a random key
// rather than a human-written name: long, with mixed case and digits, and
// using '_' or '-' separators.
func secretShaped(id string) bool {
	return len(normalizeToken(id)) >= 20 && mixedCase(id) && strings.ContainsAny(id, "_-")
}

// matchesDenyTerm reports whether any private deny-list term occurs inside
// the identifier, ignoring case and separators.
func (p Policy) matchesDenyTerm(id string) bool {
	n := normalizeAlnum(id)
	for _, term := range p.DenyTerms {
		if t := normalizeAlnum(term); len(t) >= 3 && strings.Contains(n, t) {
			return true
		}
	}
	return false
}

// sensitiveInRemovedLine classifies the identifiers on one removed line
// (without its leading '-') that are sensitive values:
//   - scalars assigned in data files (config values, namespaces, ids)
//   - segments of /home paths (usernames, private workspace directories)
//   - labels of dot-separated domains and internal hosts
//   - deny-list matches, which count even inside code identifiers
//   - secret-shaped strings (long, mixed case, with separators)
//
// Ordinary code identifiers and the words of code comments are never
// sensitive on their own.
func (p Policy) sensitiveInRemovedLine(line string, inDataFile bool) map[string]bool {
	out := map[string]bool{}
	add := func(text string) {
		for _, id := range identRe.FindAllString(text, -1) {
			if n := normalizeToken(id); considerable(n) && !commonWords[n] {
				out[n] = true
			}
		}
	}
	if inDataFile {
		if m := dataValueRe.FindStringSubmatch(line); m != nil && len(m[1]) <= maxDataValueLen {
			for _, id := range identRe.FindAllString(m[1], -1) {
				n := normalizeToken(id)
				if considerable(n) && !commonWords[n] && (identifierShaped(id) || len(n) >= minSensitiveValueLen) {
					out[n] = true
				}
			}
		}
	}
	for _, m := range homePathRe.FindAllString(line, -1) {
		add(m)
	}
	for _, m := range domainRe.FindAllString(line, -1) {
		add(m)
	}
	for _, id := range identRe.FindAllString(line, -1) {
		if secretShaped(id) || p.matchesDenyTerm(id) {
			if n := normalizeToken(id); considerable(n) && !commonWords[n] {
				out[n] = true
			}
		}
	}
	return out
}

func (p Policy) scanDiff(diff string) diffTokens {
	t := diffTokens{
		removed: map[string]bool{}, added: map[string]bool{}, header: map[string]bool{},
		removedW: map[string]bool{}, addedW: map[string]bool{},
		sensitiveRemoved: map[string]bool{}, sensitiveRemovedW: map[string]bool{},
	}
	sc := bufio.NewScanner(strings.NewReader(diff))
	sc.Buffer(make([]byte, 1<<20), 16<<20)
	inDataFile := false
	for sc.Scan() {
		line := sc.Text()
		var ids, words map[string]bool
		var removedLine bool
		switch {
		case strings.HasPrefix(line, "+++") || strings.HasPrefix(line, "---"):
			ids = t.header
			if path := diffPathLine(line); path != "" {
				inDataFile = isDataFilePath(path)
			}
		case strings.HasPrefix(line, "diff --git") || strings.HasPrefix(line, "rename ") ||
			strings.HasPrefix(line, "similarity ") || strings.HasPrefix(line, "index "):
			ids = t.header
			if path := diffGitHeaderPath(line); path != "" {
				inDataFile = isDataFilePath(path)
			}
		case strings.HasPrefix(line, "+"):
			ids, words = t.added, t.addedW
			line = line[1:]
		case strings.HasPrefix(line, "-"):
			ids, words = t.removed, t.removedW
			line = line[1:]
			removedLine = true
		default:
			continue
		}
		var sens map[string]bool
		if removedLine {
			sens = p.sensitiveInRemovedLine(line, inDataFile)
		}
		for _, id := range identRe.FindAllString(line, -1) {
			n := normalizeToken(id)
			if !considerable(n) {
				continue
			}
			ids[n] = true
			if words != nil {
				for _, w := range splitWords(id) {
					if considerable(w) {
						words[w] = true
					}
				}
			}
			if sens[n] {
				t.sensitiveRemoved[n] = true
				for _, w := range splitWords(id) {
					if considerable(w) {
						t.sensitiveRemovedW[w] = true
					}
				}
			}
		}
	}
	return t
}

// RemovedOnlyHits counts message tokens (4+ characters) that repeat a
// sensitive value occurring only on removed diff lines: configuration values,
// /home paths, domains, secret-shaped strings, and deny-list names. Ordinary
// identifiers that merely disappeared from the code (a renamed function, a
// deleted helper) and the words inside removed comments are not sensitive,
// so naming them is allowed. Identifiers and their camelCase or snake_case
// words are compared case- and separator-insensitively. Paths in diff headers
// count as present, so naming a deleted file is allowed.
func (p Policy) RemovedOnlyHits(message, diff string) int {
	if strings.TrimSpace(diff) == "" {
		return 0
	}
	t := p.scanDiff(diff)
	seen := map[string]bool{}
	hits := 0
	for _, id := range identRe.FindAllString(message, -1) {
		key := normalizeToken(id)
		if seen[key] {
			continue
		}
		seen[key] = true
		shaped := identifierShaped(id)
		for ci, cand := range append([]string{key}, splitWords(id)...) {
			if !considerable(cand) {
				continue
			}
			// A plain word, or a word part of an identifier, that is ordinary
			// English carries no name. The whole identifier still counts.
			if (ci > 0 || !shaped) && commonWords[cand] {
				continue
			}
			whole := t.sensitiveRemoved[cand] && !t.added[cand] && !t.header[cand]
			word := t.sensitiveRemovedW[cand] && !t.addedW[cand] && !t.added[cand] && !t.header[cand]
			if whole || word {
				hits++
				break
			}
		}
	}
	return hits
}

// RemovedOnlyTerms lists the normalized identifiers that occur only on
// removed lines of diff and classify as sensitive values. It exists for
// evaluation and tests; generation code must never log or echo its result.
func (p Policy) RemovedOnlyTerms(diff string) []string {
	t := p.scanDiff(diff)
	var terms []string
	for id := range t.sensitiveRemoved {
		if !t.added[id] && !t.header[id] && !commonWords[id] {
			terms = append(terms, id)
		}
	}
	sort.Strings(terms)
	return terms
}

// DenyHits counts private deny-list terms that appear in text, ignoring case
// and separators.
func (p Policy) DenyHits(text string) int {
	norm := normalizeAlnum(text)
	hits := 0
	for _, term := range p.DenyTerms {
		n := normalizeAlnum(term)
		if len(n) >= 3 && strings.Contains(norm, n) {
			hits++
		}
	}
	return hits
}

func normalizeAlnum(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return b.String()
}

var (
	emailRe   = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9-]+(?:\.[A-Za-z0-9-]+)+`)
	ipv4Re    = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)
	ipv6Re    = regexp.MustCompile(`[0-9A-Fa-f:]{3,}:[0-9A-Fa-f:.]*`)
	secretRes = []*regexp.Regexp{
		regexp.MustCompile(`\bAKIA[0-9A-Z]{16}\b`),
		regexp.MustCompile(`\bgh[pousr]_[A-Za-z0-9]{20,}\b`),
		regexp.MustCompile(`\bgithub_pat_[A-Za-z0-9_]{20,}\b`),
		regexp.MustCompile(`\bsk-[A-Za-z0-9_-]{20,}\b`),
		regexp.MustCompile(`\bxox[abprs]-[A-Za-z0-9-]{10,}\b`),
		regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{5,}\b`),
		regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`),
		regexp.MustCompile(`\b[0-9a-fA-F]{40,}\b`),
		regexp.MustCompile(`(?i)\b(?:api[_-]?key|secret|token|passw(?:or)?d)\s*[:=]\s*\S{8,}`),
	}
)

var base64Re = regexp.MustCompile(`[A-Za-z0-9+/]{40,}={0,2}`)

// mixedCase reports whether s has letters of both cases and a digit, which
// separates random tokens from long paths and identifiers.
func mixedCase(s string) bool {
	var up, low, dig bool
	for _, r := range s {
		switch {
		case unicode.IsUpper(r):
			up = true
		case unicode.IsLower(r):
			low = true
		case unicode.IsDigit(r):
			dig = true
		}
	}
	return up && low && dig
}

func allowedIPv4(s string) bool {
	ip := net.ParseIP(s)
	if ip == nil {
		return true // not a valid address, such as a version string like 1.2.3.999
	}
	for _, cidr := range []string{"192.0.2.0/24", "198.51.100.0/24", "203.0.113.0/24", "127.0.0.0/8", "0.0.0.0/32"} {
		if _, n, _ := net.ParseCIDR(cidr); n.Contains(ip) {
			return true
		}
	}
	return false
}

// sensitivePatterns reports emails, IP addresses, internal hosts, and secrets.
func (p Policy) sensitivePatterns(text string) []Finding {
	var out []Finding
	if n := len(emailRe.FindAllString(text, -1)); n > 0 {
		out = append(out, Finding{RuleEmail, "the message contains an email address", n})
	}
	ips := 0
	for _, m := range ipv4Re.FindAllString(text, -1) {
		if !allowedIPv4(m) {
			ips++
		}
	}
	for _, m := range ipv6Re.FindAllString(text, -1) {
		m = strings.Trim(m, ".")
		if ip := net.ParseIP(m); ip != nil && strings.Count(m, ":") >= 2 && !ip.IsLoopback() && !ip.IsUnspecified() && !strings.HasPrefix(strings.ToLower(m), "2001:db8") {
			ips++
		}
	}
	if ips > 0 {
		out = append(out, Finding{RuleIP, "the message contains an IP address (use 192.0.2.0/24 or 2001:db8::/32 placeholders)", ips})
	}
	if p.HostPattern != nil {
		if n := len(p.HostPattern.FindAllString(text, -1)); n > 0 {
			out = append(out, Finding{RuleHost, "the message names an internal hostname", n})
		}
	}
	secrets := 0
	for _, re := range secretRes {
		secrets += len(re.FindAllString(text, -1))
	}
	for _, m := range base64Re.FindAllString(text, -1) {
		if mixedCase(m) {
			secrets++
		}
	}
	if secrets > 0 {
		out = append(out, Finding{RuleSecret, "the message contains a key or token pattern", secrets})
	}
	return out
}

// Check runs every leak rule against message, given the staged diff. It never
// echoes matched text in the findings.
func (p Policy) Check(message, diff string) []Finding {
	var out []Finding
	if n := p.DenyHits(message); n > 0 {
		out = append(out, Finding{RuleDenyList, "the message contains a term from the private deny-list", n})
	}
	if n := p.RemovedOnlyHits(message, diff); n > 0 {
		out = append(out, Finding{RuleRemovedEcho, fmt.Sprintf("the message repeats %d sensitive value(s) that appear only on removed lines of the diff", n), n})
	}
	out = append(out, p.sensitivePatterns(message)...)
	return out
}

// LoadPolicy reads the private deny-lists and the host pattern. repoDir may be
// empty. Missing files are not errors. Terms are never logged.
func LoadPolicy(repoDir string) Policy {
	var p Policy
	if home, err := os.UserHomeDir(); err == nil {
		p.DenyTerms = append(p.DenyTerms, readTermFile(filepath.Join(home, ".buckley", "private-terms"))...)
	}
	if path := gitInfoPath(repoDir, "info/buckley-private"); path != "" {
		p.DenyTerms = append(p.DenyTerms, readTermFile(path)...)
	}
	pattern := strings.TrimSpace(os.Getenv("BUCKLEY_INTERNAL_HOST_PATTERN"))
	if pattern == "" {
		if home, err := os.UserHomeDir(); err == nil {
			if b, err := os.ReadFile(filepath.Join(home, ".buckley", "internal-host-pattern")); err == nil {
				pattern = strings.TrimSpace(string(b))
			}
		}
	}
	if pattern == "" {
		pattern = DefaultInternalHostPattern
	}
	if re, err := regexp.Compile(pattern); err == nil {
		p.HostPattern = re
	} else if re, err := regexp.Compile(DefaultInternalHostPattern); err == nil {
		p.HostPattern = re
	}
	return p
}

func gitInfoPath(repoDir, rel string) string {
	cmd := exec.Command("git", "rev-parse", "--git-path", rel)
	if repoDir != "" {
		cmd.Dir = repoDir
	}
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	path := strings.TrimSpace(string(out))
	if path != "" && !filepath.IsAbs(path) {
		base := repoDir
		if base == "" {
			base, _ = os.Getwd()
		}
		path = filepath.Join(base, path)
	}
	return path
}

func readTermFile(path string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var terms []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		terms = append(terms, line)
	}
	return terms
}
