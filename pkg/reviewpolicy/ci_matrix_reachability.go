package reviewpolicy

import (
	"fmt"
	"path"
	"sort"
	"strings"
)

func normalizeCIMatrixReachabilityEvidence(value CIReachabilityEvidence, expectation CIAdmissionExpectation, contexts []CIRequiredContext) (*CIReachabilityEvidence, error) {
	invalid := func(detail string) (*CIReachabilityEvidence, error) {
		return nil, fmt.Errorf("%w: %s", ErrCIAdmissionInvalid, detail)
	}
	if value.HeadSHA != expectation.Identity.HeadSHA || value.MergeSHA == "" || value.RunID <= 0 || value.JobID != 0 || value.Check != "" || value.Module != "" || len(value.Packages) != 0 || len(value.Modules) == 0 || len(expectation.TestReachability.RecognizedChangedTestFiles) == 0 {
		return invalid("incomplete or stale matrix test-reachability evidence")
	}
	required := map[string]bool{}
	for _, context := range contexts {
		required[context.Name] = context.State == "PASS" || context.State == "SUCCESS"
	}
	pending := map[string]bool{}
	for _, file := range expectation.TestReachability.RecognizedChangedTestFiles {
		pending[file] = true
	}
	jobs, dirs := map[int64]bool{}, map[string]bool{}
	value.Modules = append([]CIReachabilityModule(nil), value.Modules...)
	for index := range value.Modules {
		module := &value.Modules[index]
		dir := module.Directory
		if module.JobID <= 0 || jobs[module.JobID] || dirs[dir] || !required[module.Check] || module.Check != "Go ("+dir+")" || module.Module == "" || strings.TrimSpace(module.Module) != module.Module || path.Clean(dir) != dir || path.IsAbs(dir) || dir == ".." || strings.HasPrefix(dir, "../") || strings.Contains(dir, "\\") || len(module.TestFiles) == 0 {
			return invalid("matrix module is not bound to a distinct passing required job")
		}
		jobs[module.JobID], dirs[dir] = true, true
		module.TestFiles = append([]string(nil), module.TestFiles...)
		module.Packages = append([]string(nil), module.Packages...)
		packages := map[string]bool{}
		for _, file := range module.TestFiles {
			if !pending[file] || !strings.HasSuffix(file, "_test.go") {
				return invalid("matrix test coverage is duplicated or unrequested")
			}
			delete(pending, file)
			relative := file
			if dir != "." {
				if !strings.HasPrefix(file, dir+"/") {
					return invalid("matrix test is outside its module directory")
				}
				relative = strings.TrimPrefix(file, dir+"/")
			}
			pkg := module.Module
			if packageDir := path.Dir(relative); packageDir != "." {
				pkg += "/" + packageDir
			}
			packages[pkg] = true
		}
		for _, pkg := range module.Packages {
			if !packages[pkg] {
				return invalid("matrix package does not match a covered test")
			}
			delete(packages, pkg)
		}
		if len(packages) != 0 {
			return invalid("matrix test package is not covered")
		}
		sort.Strings(module.TestFiles)
		sort.Strings(module.Packages)
	}
	if len(pending) != 0 {
		return invalid("matrix evidence omits changed tests")
	}
	sort.Slice(value.Modules, func(i, j int) bool { return value.Modules[i].Directory < value.Modules[j].Directory })
	return &value, nil
}
