package workspaceevidence

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInspectWorkspaceLicenseRecognizesMITAndMatchesExactEvidence(t *testing.T) {
	root := t.TempDir()
	writeMITLicense(t, root, "2026 Example Contributors")

	inspection, err := InspectWorkspaceLicense(root)
	if err != nil {
		t.Fatal(err)
	}
	if inspection.Status != LicenseStatusRecognizedOSS || inspection.Evidence.ID != LicenseIDMIT {
		t.Fatalf("inspection = %+v", inspection)
	}
	if err := inspection.Evidence.Validate(); err != nil {
		t.Fatal(err)
	}
	matched, ok, err := MatchWorkspaceLicense(root, inspection.Evidence)
	if err != nil || !ok || matched.Evidence != inspection.Evidence {
		t.Fatalf("match = %+v, %v, %v", matched, ok, err)
	}

	writeMITLicense(t, root, "2027 Replacement Contributors")
	changed, ok, err := MatchWorkspaceLicense(root, inspection.Evidence)
	if err != nil || ok || changed.Status != LicenseStatusChanged {
		t.Fatalf("changed match = %+v, %v, %v", changed, ok, err)
	}
}

func TestInspectWorkspaceLicenseFailClosedStates(t *testing.T) {
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
		{name: "symlink", status: LicenseStatusUnreadable, setup: func(t *testing.T, root string) {
			writeLicense(t, root, "real-license", "MIT License\n")
			if err := os.Symlink("real-license", filepath.Join(root, "LICENSE")); err != nil {
				t.Fatal(err)
			}
		}},
		{name: "oversized", status: LicenseStatusUnreadable, setup: func(t *testing.T, root string) {
			writeLicense(t, root, "LICENSE", strings.Repeat("x", MaxLicenseFileSize+1))
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
				t.Fatal(err)
			}
			if inspection.Status != tt.status || !inspection.Evidence.IsZero() {
				t.Fatalf("inspection = %+v, want status %q and zero evidence", inspection, tt.status)
			}
		})
	}
}

func TestInspectWorkspaceLicenseDetectsSameInodeMutation(t *testing.T) {
	root := t.TempDir()
	writeMITLicense(t, root, "2026 Example Contributors")
	mutated := false
	inspection, err := InspectWorkspaceLicenseWithHook(root, func(name string) {
		if name == "LICENSE" && !mutated {
			mutated = true
			writeMITLicense(t, root, "2027 Replacement Contributors")
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	if !mutated || inspection.Status != LicenseStatusChanged || !inspection.Evidence.IsZero() {
		t.Fatalf("inspection = %+v, mutated=%v", inspection, mutated)
	}
}

func TestNormalizeWorkspaceRootResolvesAlias(t *testing.T) {
	root := t.TempDir()
	alias := filepath.Join(t.TempDir(), "workspace")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	canonical, err := NormalizeWorkspaceRoot(alias)
	if err != nil {
		t.Fatal(err)
	}
	if canonical != root {
		t.Fatalf("canonical root = %q, want %q", canonical, root)
	}
}

func TestRecognizeLicenseExactCatalog(t *testing.T) {
	mit := "MIT License\n\nCopyright (c) 2026 Example\n\n" + CanonicalMITBody
	if got := RecognizeLicense([]byte(mit)); got != LicenseIDMIT {
		t.Fatalf("MIT recognition = %q", got)
	}
	if got := RecognizeLicense([]byte(mit + "Additional restriction.\n")); got != "" {
		t.Fatalf("modified MIT recognition = %q", got)
	}
	if got := RecognizedExactLicenseDigest("c71d239df91726fc519c6eb72d318ec65820627232b2f796219e87dcf35d0ab4"); got != LicenseIDApache20 {
		t.Fatalf("Apache recognition = %q", got)
	}
}

func writeMITLicense(t *testing.T, root, holder string) {
	t.Helper()
	writeLicense(t, root, "LICENSE", "MIT License\n\nCopyright (c) "+holder+"\n\n"+CanonicalMITBody)
}

func writeLicense(t *testing.T, root, name, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, name), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}
