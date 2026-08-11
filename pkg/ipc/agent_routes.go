package ipc

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/go-chi/chi/v5"
	"m31labs.dev/buckley/pkg/agentspec"
	"m31labs.dev/buckley/pkg/headless"
	"m31labs.dev/buckley/pkg/storage"
)

// setupAgentRoutes exposes the project-local agent catalog used by Mission
// Control's workspace launcher. The daemon remains the authority for loading
// and applying profiles; the browser only receives safe metadata.
func (s *Server) setupAgentRoutes(r chi.Router) {
	r.Get("/agent-specs", s.handleListAgentSpecs)
}

func (s *Server) handleListAgentSpecs(w http.ResponseWriter, r *http.Request) {
	if _, ok := requireScope(w, r, storage.TokenScopeViewer); !ok {
		return
	}
	project, err := s.resolveAgentProjectPath(r.URL.Query().Get("project"))
	if err != nil {
		respondError(w, http.StatusBadRequest, err)
		return
	}
	discovery, err := agentspec.DiscoverProjectSpecs(project)
	if err != nil {
		respondError(w, http.StatusInternalServerError, err)
		return
	}
	respondJSON(w, map[string]any{
		"project": project,
		"root":    discovery.Root,
		"specs":   discovery.Specs,
	})
}

func (s *Server) resolveAgentProjectPath(raw string) (string, error) {
	project := strings.TrimSpace(raw)
	root := strings.TrimSpace(s.projectRoot)
	if project == "" {
		project = root
	}
	if project == "" {
		project = "."
	}
	if root != "" && !filepath.IsAbs(project) {
		project = filepath.Join(root, project)
	}
	absProject, err := filepath.Abs(project)
	if err != nil {
		return "", fmt.Errorf("resolve project path: %w", err)
	}
	if root != "" {
		rootAbs, rootErr := filepath.Abs(root)
		if rootErr != nil {
			return "", fmt.Errorf("resolve project root: %w", rootErr)
		}
		if !isWithinPath(rootAbs, absProject) {
			return "", fmt.Errorf("project path must be within %s", rootAbs)
		}
	}
	info, err := os.Stat(absProject)
	if err != nil {
		return "", fmt.Errorf("stat project path: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("project path is not a directory: %s", absProject)
	}
	return filepath.Clean(absProject), nil
}

// resolveHeadlessAgentProfile turns an agent/subagent selector into the
// daemon-owned prompt section used by a new headless runner. Selectors match
// the stable catalog name, kind, filename, or path; an empty selector chooses
// the conventional .buckley/agent.yaml when one exists.
func (s *Server) resolveHeadlessAgentProfile(project, selector, subagent string) (string, error) {
	profile, _, _, err := s.resolveHeadlessAgentSelection(project, selector, subagent)
	return profile, err
}

func (s *Server) resolveHeadlessAgentSelection(project, selector, subagent string) (string, string, *headless.ToolPolicy, error) {
	selector = strings.TrimSpace(selector)
	subagent = strings.TrimSpace(subagent)
	if selector == "" && subagent == "" {
		return "", "", nil, nil
	}
	discovery, err := agentspec.DiscoverProjectSpecs(project)
	if err != nil {
		return "", "", nil, err
	}
	if len(discovery.Specs) == 0 {
		return "", "", nil, fmt.Errorf("project agent specs not found under %s", project)
	}
	spec, err := selectDiscoveredAgentSpec(discovery.Specs, selector)
	if err != nil {
		return "", "", nil, err
	}
	if !spec.Valid {
		if spec.Error != "" {
			return "", "", nil, fmt.Errorf("agent spec %s is invalid: %s", spec.Name, spec.Error)
		}
		return "", "", nil, fmt.Errorf("agent spec %s is invalid", spec.Name)
	}
	profile, err := agentspec.LoadRuntimeProfile(spec.Path)
	if err != nil {
		return "", "", nil, err
	}
	if subagent != "" {
		profile, err = profile.SubagentProfile(subagent)
		if err != nil {
			return "", "", nil, err
		}
	}
	modelID := strings.TrimSpace(profile.Spec.Models.Execution)
	if modelID == "" {
		modelID = strings.TrimSpace(profile.Spec.Models.Chat)
	}
	return profile.PromptSection(), modelID, agentToolPolicy(profile), nil
}

// agentToolPolicy carries the selected profile's explicit tool boundaries into
// the headless runner. Prompt text is useful guidance, but the daemon must also
// enforce allow/deny lists at the registry boundary.
func agentToolPolicy(profile *agentspec.RuntimeProfile) *headless.ToolPolicy {
	if profile == nil || profile.Spec == nil {
		return nil
	}
	if len(profile.Spec.Tools.Allow) == 0 && len(profile.Spec.Tools.Deny) == 0 {
		return nil
	}
	return &headless.ToolPolicy{
		AllowedTools: append([]string(nil), profile.Spec.Tools.Allow...),
		DeniedTools:  append([]string(nil), profile.Spec.Tools.Deny...),
	}
}

func mergeHeadlessToolPolicies(request, profile *headless.ToolPolicy) *headless.ToolPolicy {
	if profile == nil {
		return request
	}
	if request == nil {
		return cloneHeadlessToolPolicy(profile)
	}

	merged := cloneHeadlessToolPolicy(request)
	if profile.AllowedTools != nil {
		if request.AllowedTools == nil {
			merged.AllowedTools = append([]string(nil), profile.AllowedTools...)
		} else {
			merged.AllowedTools = intersectToolNames(request.AllowedTools, profile.AllowedTools)
			if len(merged.AllowedTools) == 0 {
				// A non-nil empty allow list is an explicit deny-all sentinel;
				// applyToolPolicy preserves that distinction at the registry edge.
				merged.AllowedTools = make([]string, 0)
			}
		}
	}
	merged.DeniedTools = appendUniqueToolNames(request.DeniedTools, profile.DeniedTools...)
	merged.RequireApproval = appendUniqueToolNames(request.RequireApproval, profile.RequireApproval...)
	merged.MaxExecTimeSeconds = stricterPositiveInt(request.MaxExecTimeSeconds, profile.MaxExecTimeSeconds)
	merged.MaxFileSizeBytes = stricterPositiveInt64(request.MaxFileSizeBytes, profile.MaxFileSizeBytes)
	return merged
}

func cloneHeadlessToolPolicy(policy *headless.ToolPolicy) *headless.ToolPolicy {
	if policy == nil {
		return nil
	}
	clone := *policy
	clone.AllowedTools = append([]string(nil), policy.AllowedTools...)
	clone.DeniedTools = append([]string(nil), policy.DeniedTools...)
	clone.RequireApproval = append([]string(nil), policy.RequireApproval...)
	return &clone
}

func intersectToolNames(left, right []string) []string {
	rightSet := make(map[string]struct{}, len(right))
	for _, name := range right {
		if name = strings.TrimSpace(name); name != "" {
			rightSet[name] = struct{}{}
		}
	}
	result := make([]string, 0, len(left))
	seen := make(map[string]struct{}, len(left))
	for _, name := range left {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, ok := rightSet[name]; ok {
			if _, duplicate := seen[name]; !duplicate {
				result = append(result, name)
				seen[name] = struct{}{}
			}
		}
	}
	return result
}

func appendUniqueToolNames(base []string, names ...string) []string {
	result := append([]string(nil), base...)
	seen := make(map[string]struct{}, len(result)+len(names))
	for i, name := range result {
		name = strings.TrimSpace(name)
		result[i] = name
		if name != "" {
			seen[name] = struct{}{}
		}
	}
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		result = append(result, name)
		seen[name] = struct{}{}
	}
	return result
}

func stricterPositiveInt(left, right int32) int32 {
	if left <= 0 {
		return right
	}
	if right <= 0 || left < right {
		return left
	}
	return right
}

func stricterPositiveInt64(left, right int64) int64 {
	if left <= 0 {
		return right
	}
	if right <= 0 || left < right {
		return left
	}
	return right
}

func selectDiscoveredAgentSpec(specs []agentspec.DiscoveredSpec, selector string) (agentspec.DiscoveredSpec, error) {
	selector = strings.TrimSpace(selector)
	if selector == "" {
		for _, spec := range specs {
			base := strings.ToLower(filepath.Base(spec.Path))
			parent := strings.ToLower(filepath.Base(filepath.Dir(spec.Path)))
			if (base == "agent.yaml" || base == "agent.yml") && parent == ".buckley" {
				return spec, nil
			}
		}
		if len(specs) == 1 {
			return specs[0], nil
		}
		return agentspec.DiscoveredSpec{}, fmt.Errorf("multiple project agent specs found; choose an agent")
	}

	matches := make([]agentspec.DiscoveredSpec, 0, 1)
	for _, spec := range specs {
		if discoveredAgentSpecMatches(spec, selector) {
			matches = append(matches, spec)
		}
	}
	switch len(matches) {
	case 0:
		return agentspec.DiscoveredSpec{}, fmt.Errorf("project agent spec %q not found", selector)
	case 1:
		return matches[0], nil
	default:
		return agentspec.DiscoveredSpec{}, fmt.Errorf("project agent spec %q is ambiguous", selector)
	}
}

func discoveredAgentSpecMatches(spec agentspec.DiscoveredSpec, selector string) bool {
	selector = strings.TrimSpace(selector)
	path := strings.TrimSpace(spec.Path)
	if path == selector || filepath.ToSlash(path) == filepath.ToSlash(selector) {
		return true
	}
	if spec.Name == selector || spec.Kind == selector {
		return true
	}
	stem := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	return stem == selector
}
