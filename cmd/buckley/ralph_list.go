package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

func runRalphList(args []string) error {
	fs := flag.NewFlagSet("ralph list", flag.ContinueOnError)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: buckley ralph list [flags]\n\n")
		fmt.Fprintf(os.Stderr, "List Ralph sessions from log files.\n\n")
		fmt.Fprintf(os.Stderr, "Flags:\n")
		fs.PrintDefaults()
	}

	logDir := fs.String("log-dir", "", "Directory containing ralph runs (overrides project detection)")
	project := fs.String("project", "", "Project name (default: current directory's project)")
	allProjects := fs.Bool("all-projects", false, "Show sessions from all projects")
	all := fs.Bool("all", false, "Show all sessions including completed")

	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return nil
		}
		return err
	}

	// Determine runs directory
	runsDir := *logDir
	if runsDir == "" {
		ralphDataDir, err := getRalphDataDir()
		if err != nil {
			return fmt.Errorf("get ralph data directory: %w", err)
		}

		if *allProjects {
			// List from all projects
			return listAllProjectSessions(ralphDataDir, *all)
		}

		// Get project name
		projectName := *project
		if projectName == "" {
			cwd, err := os.Getwd()
			if err != nil {
				return fmt.Errorf("get working directory: %w", err)
			}
			projectName = getProjectName(cwd)
		}
		runsDir = filepath.Join(ralphDataDir, "projects", projectName, "runs")
	}

	// Check if directory exists
	if _, err := os.Stat(runsDir); os.IsNotExist(err) {
		fmt.Println("No ralph sessions found.")
		return nil
	}

	// List run directories
	entries, err := os.ReadDir(runsDir)
	if err != nil {
		return fmt.Errorf("reading runs directory: %w", err)
	}

	var sessions []sessionInfo

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}

		sessionID := entry.Name()
		logPath := filepath.Join(runsDir, sessionID, "log.jsonl")

		// Check if log file exists
		if _, err := os.Stat(logPath); os.IsNotExist(err) {
			continue
		}

		info, err := parseSessionLog(logPath)
		if err != nil {
			continue // Skip unparseable files
		}

		info.ID = sessionID

		// Filter based on flags
		if !*all && info.Status == "completed" {
			continue
		}

		sessions = append(sessions, info)
	}

	if len(sessions) == 0 {
		fmt.Println("No ralph sessions found.")
		return nil
	}

	// Sort by start time (newest first)
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].StartTime.After(sessions[j].StartTime)
	})

	// Print header
	fmt.Printf("%-10s  %-12s  %-8s  %-6s  %-10s  %s\n",
		"SESSION", "STARTED", "STATUS", "ITERS", "COST", "PROMPT")
	fmt.Println(strings.Repeat("-", 80))

	// Print sessions
	for _, s := range sessions {
		prompt := s.Prompt
		if len(prompt) > 30 {
			prompt = prompt[:27] + "..."
		}
		prompt = strings.ReplaceAll(prompt, "\n", " ")

		fmt.Printf("%-10s  %-12s  %-8s  %-6d  $%-9.4f  %s\n",
			s.ID,
			s.StartTime.Format("01-02 15:04"),
			s.Status,
			s.Iters,
			s.Cost,
			prompt,
		)
	}

	return nil
}

// watchOptions bundles parameters for runRalphWatch.
type watchOptions struct {
	promptFile    string
	workDir       string
	dirFlag       string
	timeout       time.Duration
	maxIterations int
	noRefine      bool
	modelOverride string
	verifyCommand string
	autoCommit    bool
	createPR      bool
}
