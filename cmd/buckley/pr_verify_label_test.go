package main

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/oneshot/commands"
)

type fakeLabelClient struct {
	labels  []string
	created map[string]string
	added   []string
	removed []string
	edits   int
	reads   int
}

func (f *fakeLabelClient) EnsureLabel(_ context.Context, name, description, color string) error {
	if f.created == nil {
		f.created = map[string]string{}
	}
	f.created[name] = description
	return nil
}
func (f *fakeLabelClient) PRLabels(context.Context, string) ([]string, error) {
	f.reads++
	return f.labels, nil
}
func (f *fakeLabelClient) EditLabels(_ context.Context, _ string, add, remove []string) error {
	f.edits++
	f.added, f.removed = add, remove
	return nil
}

func TestSetVerificationLabelAddsAndSwapsOnlyVerifyLabels(t *testing.T) {
	fake := &fakeLabelClient{labels: []string{"bug", "needs-review", commands.LabelVerifyOptional}}
	add, remove, err := setVerificationLabel(context.Background(), fake, "733", commands.VerifyReceiptOrCI)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(add, []string{commands.LabelVerifyRequired}) || !reflect.DeepEqual(remove, []string{commands.LabelVerifyOptional}) {
		t.Fatalf("add %v remove %v", add, remove)
	}
	if !strings.Contains(fake.created[commands.LabelVerifyRequired], "Buckbot") {
		t.Fatalf("label was not created with a description: %v", fake.created)
	}
	for _, r := range fake.removed {
		if r == "bug" || r == "needs-review" {
			t.Fatalf("removed an unrelated label: %v", fake.removed)
		}
	}
}

func TestSetVerificationLabelIsIdempotent(t *testing.T) {
	fake := &fakeLabelClient{labels: []string{commands.LabelVerifyRequired}}
	if _, _, err := setVerificationLabel(context.Background(), fake, "733", commands.VerifyReceiptOrCI); err != nil {
		t.Fatal(err)
	}
	if fake.edits != 0 || len(fake.created) != 0 {
		t.Fatalf("no change expected: edits=%d created=%v", fake.edits, fake.created)
	}
}

func TestSetVerificationLabel_RefusesAnEmptyPR(t *testing.T) {
	for _, pr := range []string{"", "  "} {
		t.Run(fmt.Sprintf("%q", pr), func(t *testing.T) {
			fake := &fakeLabelClient{}
			_, _, err := setVerificationLabel(context.Background(), fake, pr, commands.VerifyReceiptOrCI)
			if !errors.Is(err, errEmptyPRSelector) {
				t.Fatalf("setVerificationLabel() error = %v, want %v", err, errEmptyPRSelector)
			}
			if fake.reads != 0 || fake.edits != 0 || len(fake.created) != 0 {
				t.Fatalf("the client was used with an empty PR: reads=%d edits=%d created=%v", fake.reads, fake.edits, fake.created)
			}
		})
	}
}

func TestRunPRLabelCommand_RefusesAnEmptyPR(t *testing.T) {
	g := installFakeGh(t, "[]", "")

	var err error
	captureStdout(t, func() { err = runPRLabelCommand([]string{"--require-verification", ""}) })
	if !errors.Is(err, errEmptyPRSelector) {
		t.Fatalf("runPRLabelCommand() error = %v, want %v", err, errEmptyPRSelector)
	}
	if calls := g.calls(t); len(calls) != 0 {
		t.Fatalf("gh was called with an empty PR argument, which it reads as the current branch's PR: %v", calls)
	}
}

func TestVerificationFlagMode(t *testing.T) {
	if m, err := verificationFlagMode(true, false, false); err != nil || m != commands.VerifyReceiptOrCI {
		t.Fatalf("%q %v", m, err)
	}
	if m, err := verificationFlagMode(false, false, true); err != nil || m != commands.VerifyNone {
		t.Fatalf("%q %v", m, err)
	}
	if m, _ := verificationFlagMode(false, false, false); m != "" {
		t.Fatalf("no flag = %q", m)
	}
	if _, err := verificationFlagMode(true, false, true); err == nil {
		t.Fatal("conflicting flags accepted")
	}
}

func TestParsePRCommandOptionsVerificationLabel(t *testing.T) {
	opts, err := parsePRCommandOptions([]string{"--dry-run", "--require-verification"})
	if err != nil || opts.verificationLabel != commands.VerifyReceiptOrCI {
		t.Fatalf("%+v %v", opts, err)
	}
	if _, err := parsePRCommandOptions([]string{"--require-verification", "--no-require-verification"}); err == nil {
		t.Fatal("conflicting flags accepted")
	}
}
