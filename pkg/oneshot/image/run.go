package image

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// RunnerConfig configures the image generation runner.
type RunnerConfig struct {
	BaseURL string
	APIKey  string
	Timeout time.Duration
}

// RunOptions holds the parameters for a single image generation run.
type RunOptions struct {
	Prompt     string
	OutputPath string
	InputPath  string // Optional: path to input image for editing
	Size       string // Optional: e.g. "1920x1080"
	Model      string // Optional: override default model
}

// RunResult contains the output of an image generation run.
type RunResult struct {
	Text       string // Text portion of the model response
	OutputPath string // Where the image was written
}

// Runner orchestrates the image generation flow.
type Runner struct {
	client *Client
}

// NewRunner creates an image generation runner.
func NewRunner(cfg RunnerConfig) *Runner {
	c := NewClient(cfg.BaseURL, cfg.APIKey)
	if cfg.Timeout > 0 {
		c.SetTimeout(cfg.Timeout)
	}
	return &Runner{client: c}
}

// Run executes the image generation flow: validate, optionally read input, call API, write output.
func (r *Runner) Run(ctx context.Context, opts RunOptions) (*RunResult, error) {
	if opts.OutputPath == "" {
		return nil, fmt.Errorf("output path is required (use -o flag)")
	}
	if opts.Prompt == "" {
		return nil, fmt.Errorf("prompt is required")
	}

	// Read input image if provided
	var inputImage []byte
	if opts.InputPath != "" {
		var err error
		inputImage, err = os.ReadFile(opts.InputPath)
		if err != nil {
			return nil, fmt.Errorf("reading input image %s: %w", opts.InputPath, err)
		}
	}

	// Ensure output directory exists
	outDir := filepath.Dir(opts.OutputPath)
	if outDir != "." {
		if err := os.MkdirAll(outDir, 0755); err != nil {
			return nil, fmt.Errorf("creating output directory: %w", err)
		}
	}

	// Call API
	result, err := r.client.Generate(ctx, opts.Prompt, opts.Size, inputImage, opts.Model)
	if err != nil {
		return nil, err
	}

	// Write image to file
	if err := os.WriteFile(opts.OutputPath, result.ImageData, 0644); err != nil {
		return nil, fmt.Errorf("writing image to %s: %w", opts.OutputPath, err)
	}

	return &RunResult{
		Text:       result.Text,
		OutputPath: opts.OutputPath,
	}, nil
}
