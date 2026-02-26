package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// listAllProjectSessions lists sessions from all projects.
func listAllProjectSessions(ralphDataDir string, showAll bool) error {
	projectsDir := filepath.Join(ralphDataDir, "projects")
	if _, err := os.Stat(projectsDir); os.IsNotExist(err) {
		fmt.Println("No ralph sessions found.")
		return nil
	}

	projects, err := os.ReadDir(projectsDir)
	if err != nil {
		return fmt.Errorf("reading projects directory: %w", err)
	}

	var allSessions []sessionInfo

	for _, proj := range projects {
		if !proj.IsDir() {
			continue
		}
		projectName := proj.Name()
		runsDir := filepath.Join(projectsDir, projectName, "runs")

		entries, err := os.ReadDir(runsDir)
		if err != nil {
			continue // Skip projects without runs
		}

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
				continue
			}

			// Skip completed sessions unless --all is specified
			if !showAll && info.Status == "completed" {
				continue
			}

			allSessions = append(allSessions, sessionInfo{
				Project:   projectName,
				ID:        sessionID,
				StartTime: info.StartTime,
				Status:    info.Status,
				Iters:     info.Iters,
				Cost:      info.Cost,
				Prompt:    info.Prompt,
			})
		}
	}

	if len(allSessions) == 0 {
		fmt.Println("No ralph sessions found.")
		return nil
	}

	// Sort by start time (newest first)
	sort.Slice(allSessions, func(i, j int) bool {
		return allSessions[i].StartTime.After(allSessions[j].StartTime)
	})

	// Print header
	fmt.Printf("%-15s  %-10s  %-12s  %-8s  %-6s  %-10s  %s\n",
		"PROJECT", "SESSION", "STARTED", "STATUS", "ITERS", "COST", "PROMPT")
	fmt.Println(strings.Repeat("-", 100))

	// Print sessions
	for _, s := range allSessions {
		prompt := s.Prompt
		if len(prompt) > 25 {
			prompt = prompt[:22] + "..."
		}
		prompt = strings.ReplaceAll(prompt, "\n", " ")

		projectName := s.Project
		if len(projectName) > 15 {
			projectName = projectName[:12] + "..."
		}

		fmt.Printf("%-15s  %-10s  %-12s  %-8s  %-6d  $%-9.4f  %s\n",
			projectName,
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
