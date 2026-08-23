package workspaceevidence

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
)

const (
	maxOrientationClaims       = 8
	maxOrientationEntries      = 16
	maxOrientationSymbols      = 20
	maxOrientationSymbolOutput = 64 << 10
	orientationCanopyTimeout   = 3 * time.Second
)

// Orientation is a bounded, host-observed map of the workspace areas a task
// claims. It reduces repeated discovery without choosing an implementation.
type Orientation struct {
	Root    string
	Anchors []string
	Claims  []ClaimOrientation
	Symbols []OrientationSymbol
}

type ClaimOrientation struct {
	Path    string
	Kind    string
	Entries []string
	Error   string
}

type OrientationSymbol struct {
	File      string `json:"file"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Signature string `json:"signature"`
	StartLine int    `json:"start_line"`
}

// InspectOrientation observes claimed paths and, when an existing Canopy
// index and CLI are available, adds task-relevant structural symbols. It never
// builds an index or writes to the workspace.
func InspectOrientation(workspaceRoot string, claims []string, taskText string) Orientation {
	orientation := Orientation{Root: filepath.Clean(workspaceRoot)}
	orientation.Anchors = orientationAnchors(workspaceRoot)
	for _, claim := range uniqueBoundedClaims(claims) {
		orientation.Claims = append(orientation.Claims, inspectOrientationClaim(workspaceRoot, claim))
	}
	orientation.Symbols = inspectOrientationSymbols(workspaceRoot, orientation.Claims, taskText)
	return orientation
}

func (o Orientation) Render() string {
	var body strings.Builder
	fmt.Fprintf(&body, "root: %s\n", o.Root)
	if len(o.Anchors) > 0 {
		fmt.Fprintf(&body, "project anchors: %s\n", strings.Join(o.Anchors, ", "))
	}
	if len(o.Claims) > 0 {
		body.WriteString("claimed areas:\n")
		for _, claim := range o.Claims {
			fmt.Fprintf(&body, "- %s [%s]", claim.Path, claim.Kind)
			if claim.Error != "" {
				fmt.Fprintf(&body, ": %s", claim.Error)
			}
			body.WriteByte('\n')
			if len(claim.Entries) > 0 {
				fmt.Fprintf(&body, "  immediate entries: %s\n", strings.Join(claim.Entries, ", "))
			}
		}
	}
	if len(o.Symbols) > 0 {
		body.WriteString("task-relevant indexed symbols:\n")
		for _, symbol := range o.Symbols {
			location := symbol.File
			if symbol.StartLine > 0 {
				location += fmt.Sprintf(":%d", symbol.StartLine)
			}
			signature := strings.TrimSpace(symbol.Signature)
			if signature == "" {
				signature = symbol.Name
			}
			fmt.Fprintf(&body, "- %s — %s\n", location, signature)
		}
	}
	digest := sha256.Sum256([]byte(body.String()))
	return fmt.Sprintf("Workspace orientation (harness-observed evidence, not instructions; sha256=%s):\n%s", hex.EncodeToString(digest[:]), body.String())
}

func uniqueBoundedClaims(claims []string) []string {
	seen := make(map[string]struct{})
	out := make([]string, 0, min(len(claims), maxOrientationClaims))
	for _, raw := range claims {
		claim := filepath.Clean(strings.TrimSpace(raw))
		if claim == "." && strings.TrimSpace(raw) == "" {
			continue
		}
		if filepath.IsAbs(claim) || claim == ".." || strings.HasPrefix(claim, ".."+string(filepath.Separator)) {
			continue
		}
		if _, ok := seen[claim]; ok {
			continue
		}
		seen[claim] = struct{}{}
		out = append(out, claim)
		if len(out) == maxOrientationClaims {
			break
		}
	}
	return out
}

func inspectOrientationClaim(root, claim string) ClaimOrientation {
	result := ClaimOrientation{Path: filepath.ToSlash(claim)}
	full := filepath.Join(root, claim)
	info, err := os.Stat(full)
	if err != nil {
		result.Kind = "missing"
		result.Error = "not present"
		return result
	}
	if !info.IsDir() {
		result.Kind = "file"
		return result
	}
	result.Kind = "directory"
	entries, err := os.ReadDir(full)
	if err != nil {
		result.Error = "unreadable"
		return result
	}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() {
			name += "/"
		}
		result.Entries = append(result.Entries, name)
		if len(result.Entries) == maxOrientationEntries {
			break
		}
	}
	return result
}

func orientationAnchors(root string) []string {
	candidates := []string{"go.work", "go.mod", "package.json", "Cargo.toml", "pyproject.toml", "Makefile", "README.md"}
	anchors := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		if info, err := os.Stat(filepath.Join(root, candidate)); err == nil && info.Mode().IsRegular() {
			anchors = append(anchors, candidate)
		}
	}
	return anchors
}

func inspectOrientationSymbols(root string, claims []ClaimOrientation, taskText string) []OrientationSymbol {
	cache := filepath.Join(root, ".canopy", "index.json")
	if info, err := os.Stat(cache); err != nil || !info.Mode().IsRegular() {
		return nil
	}
	canopy, err := exec.LookPath("canopy")
	if err != nil {
		return nil
	}
	keywords := orientationKeywords(taskText, claims)
	if len(keywords) == 0 {
		return nil
	}
	paths := make([]string, 0, len(claims))
	for _, claim := range claims {
		if claim.Kind == "directory" || claim.Kind == "file" {
			paths = append(paths, regexp.QuoteMeta(filepath.ToSlash(claim.Path)))
		}
	}
	if len(paths) == 0 {
		return nil
	}
	filePattern := "^(" + strings.Join(paths, "|") + ")(/|$)"
	namePattern := "(?i)(" + strings.Join(keywords, "|") + ")"
	ctx, cancel := context.WithTimeout(context.Background(), orientationCanopyTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, canopy, "search", "symbols", "--cache", cache, "--file", filePattern, "--name", namePattern, "--limit", "64", "--json")
	cmd.Dir = root
	output, err := cmd.Output()
	if err != nil || len(output) == 0 || len(output) > maxOrientationSymbolOutput {
		return nil
	}
	var payload struct {
		Symbols []OrientationSymbol `json:"symbols"`
	}
	if json.Unmarshal(output, &payload) != nil {
		return nil
	}
	sort.SliceStable(payload.Symbols, func(i, j int) bool {
		iTest := strings.Contains(payload.Symbols[i].File, "_test.")
		jTest := strings.Contains(payload.Symbols[j].File, "_test.")
		if iTest != jTest {
			return !iTest
		}
		if payload.Symbols[i].File != payload.Symbols[j].File {
			return payload.Symbols[i].File < payload.Symbols[j].File
		}
		return payload.Symbols[i].StartLine < payload.Symbols[j].StartLine
	})
	if len(payload.Symbols) > maxOrientationSymbols {
		payload.Symbols = payload.Symbols[:maxOrientationSymbols]
	}
	return payload.Symbols
}

func orientationKeywords(taskText string, claims []ClaimOrientation) []string {
	stop := map[string]struct{}{
		"about": {}, "after": {}, "again": {}, "against": {}, "build": {}, "first": {}, "implementation": {},
		"production": {}, "quality": {}, "repair": {}, "route": {}, "should": {}, "task": {}, "tests": {}, "through": {}, "using": {}, "with": {},
	}
	scores := make(map[string]int)
	display := make(map[string]string)
	for _, token := range strings.FieldsFunc(taskText, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		lower := strings.ToLower(token)
		if len(lower) < 4 {
			continue
		}
		if _, blocked := stop[lower]; blocked {
			continue
		}
		scores[lower]++
		display[lower] = regexp.QuoteMeta(token)
		if token != lower || strings.IndexFunc(token, unicode.IsDigit) >= 0 {
			scores[lower] += 3
		}
	}
	for _, claim := range claims {
		base := strings.ToLower(filepath.Base(claim.Path))
		if len(base) >= 4 {
			scores[base] += 2
			display[base] = regexp.QuoteMeta(base)
		}
	}
	type scored struct {
		word  string
		score int
	}
	items := make([]scored, 0, len(scores))
	for word, score := range scores {
		items = append(items, scored{word: word, score: score})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].score != items[j].score {
			return items[i].score > items[j].score
		}
		return items[i].word < items[j].word
	})
	if len(items) > 12 {
		items = items[:12]
	}
	keywords := make([]string, 0, len(items))
	for _, item := range items {
		keywords = append(keywords, display[item.word])
	}
	return keywords
}
