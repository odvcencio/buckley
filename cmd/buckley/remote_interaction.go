package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

func remoteInputLoop(ctx context.Context, client *remoteClient, sessionID string, sessionToken string, planID string) error {
	for {
		line, readErr := readLineWithContext(ctx, "remote> ")
		if readErr != nil {
			if errors.Is(readErr, context.Canceled) {
				fmt.Println("\nClosing remote session.")
				return nil
			}
			if errors.Is(readErr, io.EOF) {
				return nil
			}
			return fmt.Errorf("reading remote input: %w", readErr)
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if line == ":q" || line == ":quit" || line == ":exit" {
			fmt.Println("Closing remote session.")
			return nil
		}
		if strings.HasPrefix(line, ":logs") {
			if planID == "" {
				fmt.Println("No plan ID associated with this session yet.")
				continue
			}
			parts := strings.Fields(line)
			kind := "builder"
			if len(parts) > 1 {
				kind = parts[1]
			}
			logCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
			entries, err := client.fetchPlanLog(logCtx, planID, kind)
			cancel()
			if err != nil {
				fmt.Fprintf(os.Stderr, "log fetch failed: %v\n", err)
				continue
			}
			if len(entries) == 0 {
				fmt.Printf("No %s log entries\n", kind)
				continue
			}
			for _, entry := range entries {
				fmt.Printf("[%s] %s\n", kind, entry)
			}
			continue
		}
		if line == ":interrupt" {
			action := workflowActionRequest{Action: "pause", Note: "Paused via remote CLI"}
			if err := client.sendWorkflowAction(ctx, sessionID, sessionToken, action); err != nil {
				fmt.Fprintf(os.Stderr, "pause failed: %v\n", err)
			}
			continue
		}
		action, err := buildRemoteAction(line)
		if err != nil {
			fmt.Fprintf(os.Stderr, "invalid input: %v\n", err)
			continue
		}
		if err := client.sendWorkflowAction(ctx, sessionID, sessionToken, action); err != nil {
			fmt.Fprintf(os.Stderr, "action failed: %v\n", err)
		}
	}
}

func readLineWithContext(ctx context.Context, prompt string) (string, error) {
	lineCh := make(chan string, 1)
	errCh := make(chan error, 1)

	go func() {
		scanner := bufio.NewScanner(os.Stdin)
		if scanner.Scan() {
			lineCh <- scanner.Text()
			return
		}
		if err := scanner.Err(); err != nil {
			errCh <- err
			return
		}
		errCh <- io.EOF
	}()

	if strings.TrimSpace(prompt) != "" {
		fmt.Print(prompt)
	}

	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case err := <-errCh:
		return "", fmt.Errorf("reading input line: %w", err)
	case line := <-lineCh:
		return line, nil
	}
}

func openBrowser(target string) error {
	if strings.TrimSpace(target) == "" {
		return nil
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", target)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", target)
	default:
		cmd = exec.Command("xdg-open", target)
	}
	return cmd.Start()
}

func buildRemoteAction(input string) (workflowActionRequest, error) {
	input = strings.TrimSpace(input)
	switch {
	case strings.HasPrefix(input, "/"):
		return workflowActionRequest{Action: "command", Command: input}, nil
	case strings.HasPrefix(strings.ToLower(input), "plan "):
		parts := strings.SplitN(input, " ", 3)
		if len(parts) < 3 {
			return workflowActionRequest{}, fmt.Errorf("usage: plan <feature> <description>")
		}
		return workflowActionRequest{
			Action:      "plan",
			FeatureName: parts[1],
			Description: strings.TrimSpace(parts[2]),
		}, nil
	case strings.HasPrefix(strings.ToLower(input), "execute"):
		parts := strings.Fields(input)
		req := workflowActionRequest{Action: "execute"}
		if len(parts) > 1 {
			req.PlanID = parts[1]
		}
		return req, nil
	case strings.HasPrefix(strings.ToLower(input), "pause"):
		note := strings.TrimSpace(input[len("pause"):])
		return workflowActionRequest{Action: "pause", Note: note}, nil
	case strings.HasPrefix(strings.ToLower(input), "resume"):
		note := strings.TrimSpace(input[len("resume"):])
		return workflowActionRequest{Action: "resume", Note: note}, nil
	default:
		return workflowActionRequest{Action: "command", Command: input}, nil
	}
}
