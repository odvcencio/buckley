package workspaceevidence

import (
	"context"
	"fmt"
)

// revalidateExactCleanOSSDispatchWorktree binds dispatch to the same exact
// commit used to form the rule and rejects Git-visible staged, tracked,
// untracked, and submodule changes. Branch names are deliberately not authority.
func revalidateExactCleanOSSDispatchWorktree(ctx context.Context, evidence RootLicenseBlobEvidence) error {
	if err := revalidateRepositoryIdentity(ctx, evidence.repository); err != nil {
		return fmt.Errorf("%w: revalidate repository before dispatch worktree inspection", ErrEvidenceStale)
	}
	if err := requireOSSDispatchHEAD(ctx, evidence); err != nil {
		return err
	}

	status, err := boundedGitOutput(
		ctx,
		evidence.repository.root,
		maxGitMetadataBytes,
		"status",
		"--porcelain=v1",
		"-z",
		"--untracked-files=all",
		"--ignore-submodules=none",
	)
	if err != nil {
		return fmt.Errorf("%w: inspect exact dispatch worktree: %v", ErrEvidenceStale, err)
	}
	if len(status) != 0 {
		return fmt.Errorf("%w: dispatch worktree is not clean", ErrEvidenceStale)
	}

	// Bracket status with HEAD checks so a ref movement during inspection fails
	// closed instead of authorizing the state observed under a different HEAD.
	if err := requireOSSDispatchHEAD(ctx, evidence); err != nil {
		return err
	}
	if err := revalidateRepositoryIdentity(ctx, evidence.repository); err != nil {
		return fmt.Errorf("%w: revalidate repository after dispatch worktree inspection", ErrEvidenceStale)
	}
	return nil
}

func requireOSSDispatchHEAD(ctx context.Context, evidence RootLicenseBlobEvidence) error {
	headOutput, err := boundedGitOutput(
		ctx,
		evidence.repository.root,
		maxGitMetadataBytes,
		"rev-parse",
		"--verify",
		"HEAD^{commit}",
	)
	if err != nil {
		return fmt.Errorf("%w: inspect exact dispatch HEAD: %v", ErrEvidenceStale, err)
	}
	head, err := singleGitLine(headOutput)
	if err != nil || head != evidence.commitOID {
		return fmt.Errorf("%w: dispatch HEAD does not match the bound commit", ErrEvidenceStale)
	}
	return nil
}
