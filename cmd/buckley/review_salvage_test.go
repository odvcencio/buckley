package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A review that fails validation after doing the work still has value.
//
// The run may have spent several hundred thousand tokens before one schema
// check rejected it. Destroying the text leaves the caller with an error and
// nothing to read. The artifact is safe to write because reviewResultFromAgent
// stamps it with markIncompleteReview, which states it is not a merge verdict,
// and leaves parsed nil so no verdict is derived from it.
func TestSalvageIncompleteReviewWritesTheArtifact(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "review.md")
	sentinel := errors.New("primary review validation failed")

	err := salvageIncompleteReview(out, sentinel, &reviewCommandResult{
		incomplete: true,
		reviewText: "> [!WARNING]\n> **Incomplete review — salvaged from completed work.**\n\n## Grade: B\n",
	})

	if !errors.Is(err, sentinel) {
		t.Fatalf("salvage must still report the failure, got %v", err)
	}
	if !strings.Contains(err.Error(), "salvaged") {
		t.Fatalf("error should say where the artifact went, got %q", err.Error())
	}
	body, readErr := os.ReadFile(out)
	if readErr != nil {
		t.Fatalf("salvaged review was not written: %v", readErr)
	}
	if !strings.Contains(string(body), "Grade: B") {
		t.Fatalf("salvaged file lost the review body: %q", string(body))
	}
	if !strings.Contains(string(body), "Incomplete review") {
		t.Fatal("salvaged file must keep the banner that says it is not a merge verdict")
	}
}

// A complete review that failed for some other reason is not salvage material,
// and neither is an empty one. Both fall through to the plain failure.
func TestSalvageIncompleteReviewSkipsWhenThereIsNothingToSave(t *testing.T) {
	dir := t.TempDir()
	sentinel := errors.New("boom")

	for _, tc := range []struct {
		name   string
		result *reviewCommandResult
	}{
		{name: "not incomplete", result: &reviewCommandResult{reviewText: "## Grade: A"}},
		{name: "incomplete but empty", result: &reviewCommandResult{incomplete: true, reviewText: "   "}},
		{name: "nil result", result: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := filepath.Join(dir, tc.name+".md")
			err := salvageIncompleteReview(out, sentinel, tc.result)
			if !errors.Is(err, sentinel) {
				t.Fatalf("want the original error, got %v", err)
			}
			if strings.Contains(err.Error(), "salvaged") {
				t.Fatalf("nothing should have been salvaged, got %q", err.Error())
			}
			if _, statErr := os.Stat(out); statErr == nil {
				t.Fatal("no file should have been written")
			}
		})
	}
}
