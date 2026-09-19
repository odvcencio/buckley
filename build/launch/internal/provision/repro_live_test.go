//go:build launch_live

package provision

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSealedWorkerImage_ReproducibleBuild(t *testing.T) {
	if os.Getenv("BUCKLEY_LAUNCH_REPRO_LIVE") != "1" {
		t.Skip("set BUCKLEY_LAUNCH_REPRO_LIVE=1 to run two sealed image builds")
	}
	roots := SourceRoots{
		GSXMail: os.Getenv("BUCKLEY_LAUNCH_LIVE_GSXMAIL"),
		GoSX:    os.Getenv("BUCKLEY_LAUNCH_LIVE_GOSX"),
		TQWebP:  os.Getenv("BUCKLEY_LAUNCH_LIVE_TQWEBP"),
	}
	if roots.GSXMail == "" || roots.GoSX == "" || roots.TQWebP == "" {
		t.Fatal("all three BUCKLEY_LAUNCH_LIVE_* source roots are required")
	}
	dockerConfig := t.TempDir()
	runner := ExecRunner{DockerConfig: dockerConfig}
	build := func() SealResult {
		contextRoot := filepath.Join(t.TempDir(), "context")
		manifest, modules, err := SynthesizeBuildContext(context.Background(), testAssetsRoot(t), contextRoot, roots)
		if err != nil {
			t.Fatalf("synthesize reproducibility context: %v", err)
		}
		artifacts := filepath.Join(t.TempDir(), "artifacts")
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Minute)
		defer cancel()
		result, err := BuildAndSeal(ctx, SealOptions{
			ContextPath: contextRoot,
			Artifacts:   artifacts,
			Manifest:    manifest,
			ModuleLock:  modules,
			Toolchain:   ExpectedToolchain(),
			Runner:      runner,
		})
		if err != nil {
			t.Fatalf("sealed reproducibility build: %v", err)
		}
		return result
	}
	first := build()
	second := build()
	firstDigest := first.Contract.Reference[strings.LastIndex(first.Contract.Reference, "@")+1:]
	secondDigest := second.Contract.Reference[strings.LastIndex(second.Contract.Reference, "@")+1:]
	if first.Contract.ImageID != second.Contract.ImageID || firstDigest != secondDigest || first.Contract.ModuleLockSHA256 != second.Contract.ModuleLockSHA256 {
		t.Fatalf("sealed builds are not reproducible: first=%+v second=%+v", first.Contract, second.Contract)
	}
}
