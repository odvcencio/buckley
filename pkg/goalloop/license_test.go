package goalloop

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInspectWorkspaceLicense_MITBindsAndDetectsChange(t *testing.T) {
	root := t.TempDir()
	writeMITLicense(t, root, "2026 Example Contributors")
	inspection, err := InspectWorkspaceLicense(root)
	if err != nil {
		t.Fatalf("InspectWorkspaceLicense: %v", err)
	}
	if inspection.Status != LicenseStatusRecognizedOSS || inspection.Evidence.ID != LicenseIDMIT {
		t.Fatalf("inspection = %+v", inspection)
	}
	if err := inspection.Evidence.Validate(); err != nil {
		t.Fatalf("Validate evidence: %v", err)
	}

	matched, ok, err := MatchWorkspaceLicense(root, inspection.Evidence)
	if err != nil || !ok || matched.Status != LicenseStatusRecognizedOSS {
		t.Fatalf("initial match = %+v, %v, %v", matched, ok, err)
	}
	writeMITLicense(t, root, "2027 Replacement Contributors")
	changed, ok, err := MatchWorkspaceLicense(root, inspection.Evidence)
	if err != nil || ok || changed.Status != LicenseStatusChanged {
		t.Fatalf("changed match = %+v, %v, %v", changed, ok, err)
	}
}

func TestInspectWorkspaceLicense_FailClosedStates(t *testing.T) {
	tests := []struct {
		name   string
		setup  func(*testing.T, string)
		status string
	}{
		{name: "missing", status: LicenseStatusMissing},
		{name: "unsupported", status: LicenseStatusUnsupported, setup: func(t *testing.T, root string) {
			writeLicense(t, root, "LICENSE", "GNU GENERAL PUBLIC LICENSE\n")
		}},
		{name: "proprietary", status: LicenseStatusProprietary, setup: func(t *testing.T, root string) {
			writeLicense(t, root, "LICENSE", "Proprietary and confidential. All rights reserved.\n")
		}},
		{name: "ambiguous", status: LicenseStatusAmbiguous, setup: func(t *testing.T, root string) {
			writeMITLicense(t, root, "2026 Example")
			writeLicense(t, root, "COPYING", "unknown license\n")
		}},
		{name: "license symlink", status: LicenseStatusUnreadable, setup: func(t *testing.T, root string) {
			writeLicense(t, root, "real-license", "MIT License\n")
			if err := os.Symlink("real-license", filepath.Join(root, "LICENSE")); err != nil {
				t.Fatalf("Symlink: %v", err)
			}
		}},
		{name: "oversized", status: LicenseStatusUnreadable, setup: func(t *testing.T, root string) {
			writeLicense(t, root, "LICENSE", strings.Repeat("x", maxLicenseFileSize+1))
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			if tt.setup != nil {
				tt.setup(t, root)
			}
			inspection, err := InspectWorkspaceLicense(root)
			if err != nil {
				t.Fatalf("InspectWorkspaceLicense: %v", err)
			}
			if inspection.Status != tt.status || !inspection.Evidence.IsZero() {
				t.Fatalf("inspection = %+v, want status %q and no evidence", inspection, tt.status)
			}
		})
	}
}

func TestRecognizeLicense_ExactCatalog(t *testing.T) {
	mit := "MIT License\n\nCopyright (c) 2026 Example\n\n" + canonicalMITBody
	if got := recognizeLicense([]byte(mit)); got != LicenseIDMIT {
		t.Fatalf("MIT recognition = %q", got)
	}
	if got := recognizeLicense([]byte(mit + "Additional restriction.\n")); got != "" {
		t.Fatalf("modified MIT recognition = %q", got)
	}
	if got := recognizedExactLicenseDigest("c71d239df91726fc519c6eb72d318ec65820627232b2f796219e87dcf35d0ab4"); got != LicenseIDApache20 {
		t.Fatalf("Apache-2.0 digest recognition = %q", got)
	}
	if got := recognizedExactLicenseDigest(strings.Repeat("0", 64)); got != "" {
		t.Fatalf("unknown exact digest recognition = %q", got)
	}
}

func TestInspectWorkspaceLicense_CanonicalRootAlias(t *testing.T) {
	root := t.TempDir()
	writeMITLicense(t, root, "2026 Example")
	alias := filepath.Join(t.TempDir(), "workspace")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatalf("Symlink: %v", err)
	}
	fromRoot, err := InspectWorkspaceLicense(root)
	if err != nil {
		t.Fatalf("root inspection: %v", err)
	}
	fromAlias, err := InspectWorkspaceLicense(alias)
	if err != nil {
		t.Fatalf("alias inspection: %v", err)
	}
	if fromAlias != fromRoot {
		t.Fatalf("alias inspection = %+v, root = %+v", fromAlias, fromRoot)
	}
}

func TestInspectWorkspaceLicense_DetectsSameInodeMutationBetweenReads(t *testing.T) {
	root := t.TempDir()
	writeMITLicense(t, root, "2026 Example Contributors")
	path := filepath.Join(root, "LICENSE")
	before, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat before mutation: %v", err)
	}

	mutated := false
	inspection, err := inspectWorkspaceLicense(root, func(name string) {
		if name != "LICENSE" || mutated {
			return
		}
		mutated = true
		writeMITLicense(t, root, "2027 Example Contributors")
	})
	if err != nil {
		t.Fatalf("inspectWorkspaceLicense: %v", err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat after mutation: %v", err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("fixture replaced the license inode; want same-inode truncate/write")
	}
	if inspection.Status != LicenseStatusChanged || !inspection.Evidence.IsZero() {
		t.Fatalf("inspection = %+v, want changed with no evidence", inspection)
	}
}

func writeMITLicense(t *testing.T, root, holder string) {
	t.Helper()
	writeLicense(t, root, "LICENSE", "MIT License\n\nCopyright (c) "+holder+"\n\n"+canonicalMITBody)
}

func writeLicense(t *testing.T, root, name, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(contents), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
}
