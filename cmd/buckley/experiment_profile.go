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

	profileVersion := strings.TrimSpace(*version)
	if profileVersion == "" {
		profileVersion = "experiment-" + exp.ID
	}
	profiles, err := calibratedExperimentProfiles(context.Background(), storage.NewBehaviorProfileStore(store), calibrations, profileVersion, class, !*dryRun)
	if err != nil {
		return err
	}
	if *jsonOutput {
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		return encoder.Encode(profiles)
	}
	return writeExperimentProfiles(os.Stdout, profiles, *dryRun)
}

func calibratedExperimentProfiles(ctx context.Context, profiles *storage.BehaviorProfileStore, calibrations []experiment.ModelCalibration, version string, class modelprofile.Class, persist bool) ([]modelprofile.Profile, error) {
	result := make([]modelprofile.Profile, 0, len(calibrations))
	for _, calibration := range calibrations {
		existing, found, err := profiles.Get(ctx, calibration.ModelID, version)
		if err != nil {
			return nil, err
		}
		if found {
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

func writeExperimentProfiles(out io.Writer, profiles []modelprofile.Profile, dryRun bool) error {
	if out == nil {
		return fmt.Errorf("profile output is unavailable")
	}
	action := "stored"
	if dryRun {
		action = "preview"
	}
	writer := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(writer, "MODEL\tCLASS\tTASKS\tSUCCESS\tAVG LATENCY\tAVG TOKENS\tCOST/TASK\tCOST/SUCCESS\tPROFILE")
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
		fmt.Fprintf(writer, "%s\t%s\t%d\t%s\t%s\t%s\t%s\t%s\t%s\n",
			profile.ModelID, profile.ResolvedClass(), profile.Samples.TaskSuccess, success, latency, tokens, costPerTask, costPerSuccess, profile.Version)
	}
	if err := writer.Flush(); err != nil {
		return err
	}
	_, err := fmt.Fprintf(out, "\n%d empirical model profile(s) %s.\n", len(profiles), action)
	return err
}
