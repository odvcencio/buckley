package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	imagegen "github.com/odvcencio/buckley/pkg/oneshot/image"
)

func runImageCommand(args []string) error {
	fs := flag.NewFlagSet("image", flag.ContinueOnError)
	output := fs.String("o", "", "output file path (required)")
	fs.StringVar(output, "output", "", "output file path (required)")
	input := fs.String("input", "", "input image path for editing")
	size := fs.String("size", "", "image dimensions (e.g. 1920x1080)")
	modelFlag := fs.String("model", "", "model to use (default: google/gemini-3-pro-image-preview)")
	verbose := fs.Bool("verbose", false, "show text portion of model response")
	timeout := fs.Duration("timeout", 2*time.Minute, "request timeout")

	if err := fs.Parse(args); err != nil {
		return err
	}

	// Prompt is the remaining positional argument
	if fs.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "Usage: buckley image \"<prompt>\" -o <output.png> [flags]")
		fmt.Fprintln(os.Stderr, "\nFlags:")
		fs.PrintDefaults()
		return fmt.Errorf("prompt is required")
	}
	prompt := fs.Arg(0)

	if *output == "" {
		return fmt.Errorf("output path is required: use -o <file.png>")
	}

	// Load config for API key
	cfg, _, _, err := initDependenciesFn()
	if err != nil {
		return fmt.Errorf("init dependencies: %w", err)
	}

	apiKey := cfg.Providers.OpenRouter.APIKey
	if apiKey == "" {
		return fmt.Errorf("OPENROUTER_API_KEY is required for image generation")
	}
	baseURL := cfg.Providers.OpenRouter.BaseURL

	runner := imagegen.NewRunner(imagegen.RunnerConfig{
		BaseURL: baseURL,
		APIKey:  apiKey,
		Timeout: *timeout,
	})

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	result, err := runner.Run(ctx, imagegen.RunOptions{
		Prompt:     prompt,
		OutputPath: *output,
		InputPath:  *input,
		Size:       *size,
		Model:      *modelFlag,
	})
	if err != nil {
		return err
	}

	if *verbose && result.Text != "" {
		fmt.Println(result.Text)
	}
	fmt.Printf("Saved to %s\n", result.OutputPath)
	return nil
}
