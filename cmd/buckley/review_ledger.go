package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"m31labs.dev/buckley/pkg/config"
	"m31labs.dev/buckley/pkg/oneshot"
	"m31labs.dev/buckley/pkg/oneshot/commands"
	"m31labs.dev/buckley/pkg/orchestrator"
	"m31labs.dev/buckley/pkg/storage/reviewledger"
)

var newReviewLedger = func(cfg config.ReviewLedgerGCSConfig, queue string) orchestrator.ReviewLedger {
	return reviewledger.New(reviewledger.NewGCS(cfg.Bucket), cfg.Bucket, cfg.Prefix, queue)
}

func orchestratorReviewRecorder(cfg *config.Config) orchestrator.ReviewRecorder {
	ledger := configuredReviewLedger(cfg)
	if ledger == nil {
		return nil
	}
	return func(modelID string) func(*orchestrator.ReviewResult, string, error) {
		retryReviewLedger(ledger)
		record := reviewLedgerIdentity(time.Now(), "", "", modelID)
		return func(result *orchestrator.ReviewResult, draft string, err error) {
			captured := &reviewCommandResult{reviewText: draft}
			if result != nil {
				if data, jsonErr := json.Marshal(result); jsonErr == nil {
					captured.toolEvidence = []oneshot.AgentToolCall{{Name: "workflow_review", Result: string(data)}}
				}
				if len(result.Issues) > 0 {
					record.Findings, _ = json.Marshal(result.Issues)
				}
				captured.parsed = &commands.ParsedReview{Verdict: result.ApprovalStatus}
			}
			finishReviewLedger(ledger, record, captured, nil, err)
		}
	}
}

func configuredReviewLedger(cfg *config.Config) orchestrator.ReviewLedger {
	if cfg == nil || strings.TrimSpace(cfg.Review.Ledger.GCS.Bucket) == "" {
		return nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Warning: review ledger: %v\n", err)
		return nil
	}
	return newReviewLedger(cfg.Review.Ledger.GCS, filepath.Join(home, ".buckley", "review-ledger", "pending"))
}

func retryReviewLedger(ledger orchestrator.ReviewLedger) {
	if ledger == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := ledger.Retry(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: review ledger retry: %v\n", err)
	}
}

func reviewLedgerIdentity(start time.Time, ref, base, modelID string) orchestrator.ReviewRecord {
	id := rand.Text()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	git := func(args ...string) string {
		out, _ := exec.CommandContext(ctx, "git", args...).Output()
		return strings.TrimSpace(string(out))
	}
	repo := reviewRepositoryName(git("config", "--get", "remote.origin.url"))
	if repo == "" {
		root := git("rev-parse", "--show-toplevel")
		repo = "local/" + reviewledger.Hash([]byte(root))[:16]
	}
	head := git("rev-parse", "HEAD")
	if base == "" {
		base = "main"
		if git("rev-parse", "--verify", "main") == "" {
			base = "master"
		}
	}
	baseSHA := git("rev-parse", "--verify", base+"^{commit}")
	if ref == "" {
		ref = git("symbolic-ref", "--short", "HEAD")
	}
	return orchestrator.ReviewRecord{SchemaVersion: 1, ReviewID: id, Repository: repo, Ref: ref, BaseSHA: baseSHA, HeadSHA: head, Model: modelID, StartedAt: start.UTC(), Findings: json.RawMessage("[]"), Verification: []orchestrator.ReviewVerification{}, Evidence: []string{}}
}

func reviewRepositoryName(remote string) string {
	remote = strings.TrimSuffix(strings.TrimSpace(remote), ".git")
	if at := strings.LastIndex(remote, "@"); at >= 0 {
		remote = remote[at+1:]
	}
	remote = strings.ReplaceAll(remote, ":", "/")
	parts := strings.Split(strings.Trim(remote, "/"), "/")
	if len(parts) < 2 {
		return ""
	}
	name := strings.Join(parts[len(parts)-2:], "/")
	for _, char := range name {
		if !(char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || strings.ContainsRune("_.-/", char)) {
			return ""
		}
	}
	return name
}

func finishReviewLedger(ledger orchestrator.ReviewLedger, record orchestrator.ReviewRecord, result *reviewCommandResult, pr *commands.PRInfo, reviewErr error) {
	if ledger == nil {
		return
	}
	record.EndedAt = time.Now().UTC()
	record.Verdict = "FAILED"
	if pr != nil {
		record.Repository = pr.Repository
		record.PRNumber = pr.Number
		record.BaseSHA = pr.BaseSHA
		record.HeadSHA = pr.HeadSHA
		record.Ref = pr.HeadBranch
	}
	if reviewErr != nil {
		record.Error = reviewErr.Error()
	}
	blobs := make(map[string][]byte)
	add := func(body []byte) string {
		hash := reviewledger.Hash(body)
		if _, ok := blobs[hash]; !ok {
			record.Evidence = append(record.Evidence, hash)
		}
		blobs[hash] = body
		return hash
	}
	if result != nil {
		if result.snapshot != nil {
			record.HeadSHA = result.snapshot.Commit()
			record.BaseSHA = result.baseSHA
			record.Ref = result.reviewRef
			if patch := result.snapshot.Patch(); len(patch) > 0 {
				add(patch)
			}
			data, _ := json.Marshal(map[string]any{"snapshot_id": result.snapshot.ID(), "mode": result.snapshot.Mode(), "head_sha": result.snapshot.Commit(), "excluded_untracked": result.snapshot.ExcludedUntrackedFiles()})
			add(data)
		}
		if result.trace != nil && result.trace.Model != "" {
			record.Model = result.trace.Model
		}
		add([]byte(result.reviewText))
		parsed := result.parsed
		if parsed == nil {
			parsed = commands.ParseReview(result.reviewText)
		}
		if parsed != nil {
			if len(parsed.Findings) > 0 {
				record.Findings, _ = json.Marshal(parsed.Findings)
			}
			if reviewErr == nil && !result.incomplete && parsed.Verdict != "" {
				record.Verdict = parsed.Verdict
			}
		}
		if result.incomplete {
			record.Verdict = "INCOMPLETE"
		}
		for _, call := range result.toolEvidence {
			data, err := json.Marshal(call)
			if err != nil {
				fmt.Fprintf(os.Stderr, "Warning: review ledger tool evidence: %v\n", err)
				continue
			}
			hash := add(data)
			command, _ := call.Data["command"].(string)
			if command == "" && (call.Name == "exec_program" || call.Name == "run_shell" || call.Name == "run_shell_command" || call.Name == "run_verification") {
				command = call.Name + " " + call.Arguments
			}
			if command == "" {
				continue
			}
			status, _ := call.Data["status"].(string)
			var argv []string
			switch args := call.Data["argv"].(type) {
			case []string:
				argv = append([]string(nil), args...)
			case []any:
				for _, arg := range args {
					if text, ok := arg.(string); ok {
						argv = append(argv, text)
					}
				}
			}
			var exit *int
			switch code := call.Data["exit_code"].(type) {
			case int:
				exit = &code
			case float64:
				value := int(code)
				exit = &value
			case json.Number:
				if value, err := strconv.Atoi(string(code)); err == nil {
					exit = &value
				}
			}
			record.Verification = append(record.Verification, orchestrator.ReviewVerification{Command: command, Argv: argv, ExitCode: exit, Status: status, LogHash: hash})
		}
		for _, command := range result.commandEvidence {
			data, _ := json.Marshal(command)
			record.Verification = append(record.Verification, orchestrator.ReviewVerification{Command: command.Command, ExitCode: command.ExitCode, Status: command.Status, LogHash: add(data)})
		}
		if result.trace != nil {
			if data, err := json.Marshal(result.trace); err == nil {
				add(data)
			}
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if database, err := resolveLedgerDBPath(); err == nil {
		references, err := reviewledger.ReferencedEvidence(ctx, database, blobs)
		for _, body := range references {
			add(body)
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: review ledger evidence: %v\n", err)
			record.Error += fmt.Sprintf("; archive evidence: %v", err)
		}
	}
	if err := ledger.Record(ctx, record, blobs); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: review ledger: %v\n", err)
	}
}

func runReviewLedgerCommand(args []string) error {
	cfg, err := loadConfiguredConfig()
	if err != nil {
		return err
	}
	ledger := configuredReviewLedger(cfg)
	if ledger == nil {
		return fmt.Errorf("set review.ledger.gcs.bucket to enable the review ledger")
	}
	if len(args) > 0 && args[0] == "backfill" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		fs := flag.NewFlagSet("review ledger backfill", flag.ContinueOnError)
		evidenceRoot := fs.String("evidence", filepath.Join(home, ".buckley", "evidence"), "local evidence directory")
		database := fs.String("database", filepath.Join(home, ".buckley", "ledger.db"), "local evidence SQLite database")
		records := fs.String("records", filepath.Join(home, ".buckley", "reviews"), "directory of saved review manifests or queue envelopes")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if fs.NArg() != 0 {
			return fmt.Errorf("unexpected backfill arguments")
		}
		gcs := cfg.Review.Ledger.GCS
		archive := reviewledger.New(reviewledger.NewGCS(gcs.Bucket), gcs.Bucket, gcs.Prefix, filepath.Join(home, ".buckley", "review-ledger", "pending"))
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		counts, err := archive.Backfill(ctx, *evidenceRoot, *database, *records)
		_ = json.NewEncoder(os.Stdout).Encode(counts)
		return err
	}
	retryReviewLedger(ledger)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	return executeReviewLedgerCommand(ctx, ledger, args, os.Stdout)
}

func executeReviewLedgerCommand(ctx context.Context, ledger orchestrator.ReviewLedger, args []string, output io.Writer) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: buckley review ledger list --repo owner/repo [--pr N] | show <review-id>")
	}
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	switch args[0] {
	case "list":
		fs := flag.NewFlagSet("review ledger list", flag.ContinueOnError)
		repo := fs.String("repo", "", "owner/repo")
		pr := fs.Int("pr", 0, "PR number")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if fs.NArg() != 0 {
			return fmt.Errorf("unexpected list arguments")
		}
		records, err := ledger.List(ctx, *repo, *pr)
		if err != nil {
			return err
		}
		return encoder.Encode(records)
	case "show":
		if len(args) != 2 {
			return fmt.Errorf("usage: buckley review ledger show <review-id>")
		}
		record, err := ledger.Show(ctx, args[1])
		if err != nil {
			return err
		}
		return encoder.Encode(record)
	default:
		return fmt.Errorf("unknown review ledger command %q", args[0])
	}
}
