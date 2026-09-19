package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"m31labs.dev/buckley-launch/internal/provision"
)

const provisionOptIn = "BUCKLEY_LAUNCH_PROVISION"

type summary struct {
	Schema            string                    `json:"schema"`
	Status            string                    `json:"status"`
	Platform          string                    `json:"platform"`
	ContextSHA256     string                    `json:"context_sha256"`
	ModuleLockSHA256  string                    `json:"module_lock_sha256"`
	ToolchainSHA256   string                    `json:"toolchain_lock_sha256"`
	NetworkUsed       bool                      `json:"network_used"`
	ConfigModified    bool                      `json:"config_modified"`
	Worker            *provision.WorkerContract `json:"worker,omitempty"`
	ArtifactDirectory string                    `json:"artifact_directory,omitempty"`
	Artifacts         []string                  `json:"artifacts,omitempty"`
}

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "launch image provisioning failed")
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	return runWithOutput(ctx, args, os.Stdout)
}

func runWithOutput(ctx context.Context, args []string, outputWriter io.Writer) error {
	if outputWriter == nil {
		return errors.New("output writer is unavailable")
	}
	flags := flag.NewFlagSet("buckley-launch-provision", flag.ContinueOnError)
	flags.SetOutput(new(strings.Builder))
	assets := flags.String("assets", "", "path to build/launch assets")
	gsxmail := flags.String("gsxmail", "", "canonical gsxmail checkout")
	gosx := flags.String("gosx", "", "canonical gosx checkout")
	tqwebp := flags.String("tqwebp", "", "canonical tqwebp checkout")
	output := flags.String("output", "", "new artifact directory")
	execute := flags.Bool("execute", false, "build and seal the image")
	allowNetwork := flags.Bool("allow-network", false, "allow pinned public build inputs")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return errors.New("invalid arguments")
	}
	if *assets == "" || *gsxmail == "" || *gosx == "" || *tqwebp == "" {
		return errors.New("required source paths are missing")
	}
	if *allowNetwork != *execute {
		return errors.New("execute and network authorization must be supplied together")
	}
	if *execute && (os.Getenv(provisionOptIn) != "execute" || *output == "") {
		return errors.New("execution opt-in is missing")
	}
	if !*execute && *output != "" {
		return errors.New("plan mode cannot write an artifact directory")
	}

	roots := provision.SourceRoots{GSXMail: *gsxmail, GoSX: *gosx, TQWebP: *tqwebp}
	contextParent, err := os.MkdirTemp("", "buckley-launch-context-*")
	if err != nil {
		return errors.New("temporary context is unavailable")
	}
	defer os.RemoveAll(contextParent)
	contextPath := filepath.Join(contextParent, "context")
	operationCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	manifest, modules, err := provision.SynthesizeBuildContext(operationCtx, *assets, contextPath, roots)
	cancel()
	if err != nil {
		return err
	}
	lock, _, err := provision.LoadToolchainLock(filepath.Join(*assets, "toolchain.lock"))
	if err != nil {
		return err
	}
	result := summary{
		Schema:           "buckley.launch.provision-summary.v1",
		Status:           "plan_only",
		Platform:         provision.Platform,
		ContextSHA256:    manifest.ContextSHA256,
		ModuleLockSHA256: manifest.ModuleLockSHA256,
		ToolchainSHA256:  manifest.ToolchainSHA256,
		NetworkUsed:      false,
		ConfigModified:   false,
	}
	if *execute {
		artifactPath, err := safeArtifactPath(*output, []string{*assets, *gsxmail, *gosx, *tqwebp})
		if err != nil {
			return err
		}
		dockerConfig, err := os.MkdirTemp("", "buckley-launch-docker-config-*")
		if err != nil {
			return errors.New("isolated Docker client configuration is unavailable")
		}
		defer os.RemoveAll(dockerConfig)
		sealed, err := provision.BuildAndSeal(ctx, provision.SealOptions{
			ContextPath: contextPath,
			Artifacts:   artifactPath,
			Manifest:    manifest,
			ModuleLock:  modules,
			Toolchain:   lock,
			Runner:      provision.ExecRunner{DockerConfig: dockerConfig},
		})
		if err != nil {
			return err
		}
		result.Status = "sealed"
		result.NetworkUsed = true
		result.Worker = &sealed.Contract
		result.ArtifactDirectory = artifactPath
		result.Artifacts = []string{"buildkit-metadata.json", "module-lock.json", "toolchain-lock.json", "operator-config.yaml", "operator-contract.json", "provenance.json", "sbom.spdx.json"}
	}
	encoder := json.NewEncoder(outputWriter)
	encoder.SetEscapeHTML(true)
	return encoder.Encode(result)
}

func safeArtifactPath(raw string, forbidden []string) (string, error) {
	if raw == "" {
		return "", errors.New("artifact path is missing")
	}
	absolute, err := filepath.Abs(raw)
	if err != nil {
		return "", errors.New("artifact path is invalid")
	}
	if _, err := os.Lstat(absolute); err == nil || !errors.Is(err, os.ErrNotExist) {
		return "", errors.New("artifact path must not exist")
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(absolute))
	if err != nil || parent != filepath.Clean(filepath.Dir(absolute)) {
		return "", errors.New("artifact parent is not canonical")
	}
	resolved := filepath.Join(parent, filepath.Base(absolute))
	for _, item := range forbidden {
		canonical, err := filepath.Abs(item)
		if err != nil {
			return "", errors.New("source boundary is invalid")
		}
		canonical, err = filepath.EvalSymlinks(canonical)
		if err != nil || pathsOverlap(resolved, canonical) {
			return "", errors.New("artifact path overlaps a source checkout")
		}
	}
	return resolved, nil
}

func pathsOverlap(left, right string) bool {
	contains := func(parent, child string) bool {
		relative, err := filepath.Rel(parent, child)
		return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
	}
	return contains(left, right) || contains(right, left)
}
