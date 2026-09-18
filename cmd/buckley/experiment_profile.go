package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"m31labs.dev/buckley/pkg/experiment"
	"m31labs.dev/buckley/pkg/modelprofile"
	"m31labs.dev/buckley/pkg/storage"
)

func runExperimentProfile(args []string) error {
	identifier, flagArgs, err := splitExperimentProfileArgs(args)
	if err != nil {
		return err
	}
	fs := flag.NewFlagSet("experiment profile", flag.ContinueOnError)
	version := fs.String("version", "", "Immutable profile version (default experiment-<id>)")
	className := fs.String("class", "auto", "Model class override: auto, weak, balanced, or frontier")
	dryRun := fs.Bool("dry-run", false, "Preview profiles without storing them")
	jsonOutput := fs.Bool("json", false, "Print profiles as JSON")
	if err := fs.Parse(flagArgs); err != nil {
		return err
	}
	if identifier == "" {
		return fmt.Errorf("usage: buckley experiment profile <id|name> [--version <version>] [--class auto|weak|balanced|frontier] [--dry-run] [--json]")
	}
	class, err := parseExperimentProfileClass(*className)
	if err != nil {
		return err
	}

	store, err := initExperimentStore()
	if err != nil {
		return err
	}
	defer store.Close()
	expStore := experiment.NewStoreFromStorage(store)
	if expStore == nil {
		return fmt.Errorf("experiment store unavailable")
	}
	exp, err := loadExperimentByIdentifier(expStore, identifier)
	if err != nil {
		return err
	}
	if exp == nil {
		return fmt.Errorf("experiment not found: %s", identifier)
	}
	runs, err := expStore.ListRuns(exp.ID)
	if err != nil {
		return err
	}
	evaluations, err := expStore.ListEvaluationsByExperiment(exp.ID)
	if err != nil {
		return err
	}
	calibrations := experiment.ModelCalibrations(exp, runs, evaluations)
	if len(calibrations) == 0 {
		return fmt.Errorf("experiment %s has no terminal model runs to profile", exp.ID)
	}
	attributionCaveats := experimentCalibrationAttributionCaveats(calibrations)

	profileVersion := strings.TrimSpace(*version)
	if profileVersion == "" {
		profileVersion = "experiment-" + exp.ID
	}
	profiles, err := calibratedExperimentProfiles(context.Background(), storage.NewBehaviorProfileStore(store), calibrations, profileVersion, class, !*dryRun)
	if err != nil {
		return err
	}
	if len(profiles) == 0 {
		if len(attributionCaveats) > 0 {
			return fmt.Errorf("experiment %s has no attributable terminal model runs to profile: %s", exp.ID, strings.Join(attributionCaveats, "; "))
		}
		return fmt.Errorf("experiment %s has no terminal model runs to profile", exp.ID)
	}
	if *jsonOutput {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(profiles)
	}
	return writeExperimentProfiles(os.Stdout, profiles, *dryRun, attributionCaveats)
}

func runExperimentPromote(args []string) error {
	if len(args) != 2 {
		return fmt.Errorf("usage: buckley experiment promote <model-id> <profile-version>")
	}
	modelID := strings.TrimSpace(args[0])
	version := strings.TrimSpace(args[1])
	if modelID == "" || version == "" {
		return fmt.Errorf("usage: buckley experiment promote <model-id> <profile-version>")
	}

	store, err := initExperimentStore()
	if err != nil {
		return err
	}
	defer store.Close()
	profiles := storage.NewBehaviorProfileStore(store)
	if err := profiles.Promote(context.Background(), modelID, version); err != nil {
		return err
	}
	profile, found, err := profiles.Promoted(context.Background(), modelID)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("promoted model behavior profile not found after promotion: %s %s", modelID, version)
	}
	return writeExperimentPromotion(os.Stdout, profile)
}

func calibratedExperimentProfiles(ctx context.Context, profiles *storage.BehaviorProfileStore, calibrations []experiment.ModelCalibration, version string, class modelprofile.Class, persist bool) ([]modelprofile.Profile, error) {
	if err := validateExperimentProfileProviderKeys(calibrations); err != nil {
		return nil, err
	}
	result := make([]modelprofile.Profile, 0, len(calibrations))
	for _, calibration := range calibrations {
		if len(calibration.Observations) == 0 {
			continue
		}
		existing, found, err := profiles.Get(ctx, calibration.ModelID, version)
		if err != nil {
			return nil, err
		}
		if found {
			if existing.Provider != "" && calibration.ProviderID != "" && existing.Provider != calibration.ProviderID {
				return nil, fmt.Errorf("existing profile %s/%s provider %s conflicts with calibration provider %s", calibration.ModelID, version, existing.Provider, calibration.ProviderID)
			}
			result = append(result, existing)
			continue
		}
		base, _, err := profiles.Latest(ctx, calibration.ModelID)
		if err != nil {
			return nil, err
		}
		profile, err := experiment.CalibrateModelProfile(base, calibration, version, class)
		if err != nil {
			return nil, err
		}
		if persist {
			if err := profiles.Put(ctx, profile); err != nil {
				return nil, err
			}
		}
		result = append(result, profile)
	}
	return result, nil
}

func validateExperimentProfileProviderKeys(calibrations []experiment.ModelCalibration) error {
	seen := make(map[string]string)
	for _, calibration := range calibrations {
		if len(calibration.Observations) == 0 {
			continue
		}
		modelID := strings.TrimSpace(calibration.ModelID)
		if modelID == "" {
			continue
		}
		providerID := strings.TrimSpace(calibration.ProviderID)
		if prior, ok := seen[modelID]; ok && prior != providerID {
			return fmt.Errorf("model %s has observations from multiple providers (%s, %s); profile storage is keyed by model/version, so attribution is not written", modelID, providerLabel(prior), providerLabel(providerID))
		}
		seen[modelID] = providerID
	}
	return nil
}

func providerLabel(providerID string) string {
	if providerID == "" {
		return "unknown provider"
	}
	return providerID
}

func experimentCalibrationAttributionCaveats(calibrations []experiment.ModelCalibration) []string {
	var caveats []string
	for _, calibration := range calibrations {
		for _, caveat := range calibration.AttributionCaveats {
			caveats = append(caveats, fmt.Sprintf("%s: %s", calibration.ModelID, caveat))
		}
	}
	return caveats
}

func writeExperimentPromotion(out io.Writer, profile modelprofile.Profile) error {
	if out == nil {
		return fmt.Errorf("profile promotion output is unavailable")
	}
	digest, err := profile.Digest()
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "Promoted model behavior profile:\n  model: %s\n  profile: %s\n  digest: %s\n  class: %s\n",
		profile.ModelID, profile.Version, digest, profile.ResolvedClass())
	return err
}

func splitExperimentProfileArgs(args []string) (string, []string, error) {
	var identifier string
	flagArgs := make([]string, 0, len(args))
	for index := 0; index < len(args); index++ {
		arg := strings.TrimSpace(args[index])
		if arg == "" {
			continue
		}
		if !strings.HasPrefix(arg, "-") {
			if identifier != "" {
				return "", nil, fmt.Errorf("experiment profile accepts exactly one id or name")
			}
			identifier = arg
			continue
		}
		flagArgs = append(flagArgs, arg)
		if experimentProfileFlagNeedsValue(arg) && index+1 < len(args) {
			index++
			flagArgs = append(flagArgs, args[index])
		}
	}
	return identifier, flagArgs, nil
}

func experimentProfileFlagNeedsValue(arg string) bool {
	if strings.Contains(arg, "=") {
		return false
	}
	switch strings.TrimLeft(strings.TrimSpace(arg), "-") {
	case "version", "class":
		return true
	default:
		return false
	}
}

func parseExperimentProfileClass(value string) (modelprofile.Class, error) {
	switch class := modelprofile.Class(strings.ToLower(strings.TrimSpace(value))); class {
	case "", "auto":
		return "", nil
	case modelprofile.ClassWeak, modelprofile.ClassBalanced, modelprofile.ClassFrontier:
		return class, nil
	default:
		return "", fmt.Errorf("invalid model class %q (use auto, weak, balanced, or frontier)", value)
	}
}

func writeExperimentProfiles(out io.Writer, profiles []modelprofile.Profile, dryRun bool, attributionCaveats ...[]string) error {
	if out == nil {
		return fmt.Errorf("profile output is unavailable")
	}
	action := "stored"
	if dryRun {
		action = "preview"
	}
	writer := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "MODEL\tCLASS\tTASKS\tASSESSED\tSUCCESS\tAVG LATENCY\tAVG TOKENS\tCOST/TASK\tCOST/SUCCESS\tPROFILE")
	for _, profile := range profiles {
		success := "-"
		if profile.Samples.TaskSuccess > 0 {
			success = fmt.Sprintf("%.1f%%", profile.Metrics.TaskSuccessRate*100)
		}
		latency := "-"
		if profile.Samples.Latency > 0 {
			latency = fmt.Sprintf("%.0fms", profile.Metrics.AverageTaskLatencyMS)
		}
		tokens := "-"
		if profile.Samples.Tokens > 0 {
			tokens = fmt.Sprintf("%.0f", profile.Metrics.AverageTokensPerTask)
		}
		costPerTask, costPerSuccess := "-", "-"
		if profile.Samples.Cost > 0 {
			costPerTask = fmt.Sprintf("$%.4f", profile.Metrics.AverageCostUSDPerTask)
			if profile.Metrics.CostUSDPerSuccessfulTask > 0 {
				costPerSuccess = fmt.Sprintf("$%.4f", profile.Metrics.CostUSDPerSuccessfulTask)
			}
		}
		fmt.Fprintf(writer, "%s\t%s\t%d\t%d\t%s\t%s\t%s\t%s\t%s\t%s\n",
			profile.ModelID, profile.ResolvedClass(), profile.SampleSize, profile.Samples.TaskSuccess, success, latency, tokens, costPerTask, costPerSuccess, profile.Version)
	}
	if err := writer.Flush(); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(out, "\n%d empirical model profile(s) %s.\n", len(profiles), action); err != nil {
		return err
	}
	var caveats []string
	if len(attributionCaveats) > 0 {
		caveats = attributionCaveats[0]
	}
	if len(caveats) > 0 {
		if _, err := fmt.Fprintln(out, "\nAttribution caveats:"); err != nil {
			return err
		}
		for _, caveat := range caveats {
			if _, err := fmt.Fprintf(out, "- %s\n", caveat); err != nil {
				return err
			}
		}
	}
	return nil
}
