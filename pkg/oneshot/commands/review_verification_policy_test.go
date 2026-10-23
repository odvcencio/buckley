package commands

import (
	"reflect"
	"strings"
	"testing"
)

func TestResolveVerificationPolicyPrecedence(t *testing.T) {
	cases := []struct {
		name       string
		in         PolicyInputs
		wantMode   VerificationMode
		wantSource string
	}{
		{"default", PolicyInputs{}, VerifyNone, "default"},
		{"user config", PolicyInputs{UserConfig: "receipt"}, VerifyReceipt, "user config"},
		{"repo beats user", PolicyInputs{RepoConfig: "receipt-or-ci", UserConfig: "none"}, VerifyReceiptOrCI, "repository config"},
		{"label beats repo", PolicyInputs{Labels: []string{"bug", LabelVerifyOptional}, RepoConfig: "receipt-or-ci"}, VerifyNone, LabelVerifyOptional},
		{"flag beats label", PolicyInputs{Flag: "none", Labels: []string{LabelVerifyRequired}, RepoConfig: "receipt"}, VerifyNone, "--require-verification"},
		{"required label", PolicyInputs{Labels: []string{LabelVerifyRequired}}, VerifyReceiptOrCI, LabelVerifyRequired},
		{"receipt label", PolicyInputs{Labels: []string{LabelVerifyReceipt}}, VerifyReceipt, LabelVerifyReceipt},
		{"strictest label wins", PolicyInputs{Labels: []string{LabelVerifyOptional, LabelVerifyRequired}}, VerifyReceiptOrCI, LabelVerifyRequired},
		{"receipt beats receipt-or-ci", PolicyInputs{Labels: []string{LabelVerifyRequired, LabelVerifyReceipt}}, VerifyReceipt, LabelVerifyReceipt},
		{"receipt beats receipt-or-ci in reverse order", PolicyInputs{Labels: []string{LabelVerifyReceipt, LabelVerifyRequired, LabelVerifyOptional}}, VerifyReceipt, LabelVerifyReceipt},
		{"label case ignored", PolicyInputs{Labels: []string{"Buckbot:Verify-Required"}}, VerifyReceiptOrCI, LabelVerifyRequired},
		{"unrelated labels ignored", PolicyInputs{Labels: []string{"bug"}, RepoConfig: "receipt"}, VerifyReceipt, "repository config"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mode, source, err := ResolveVerificationPolicy(tc.in)
			if err != nil || mode != tc.wantMode || !strings.Contains(source, tc.wantSource) {
				t.Fatalf("got %q, %q, %v; want %q from %q", mode, source, err, tc.wantMode, tc.wantSource)
			}
		})
	}
	if _, _, err := ResolveVerificationPolicy(PolicyInputs{Flag: "always"}); err == nil {
		t.Error("bad flag accepted")
	}
}

func TestVerificationLabelPlanTouchesOnlyVerifyLabels(t *testing.T) {
	add, remove, err := VerificationLabelPlan(VerifyReceiptOrCI, []string{"bug", LabelVerifyOptional})
	if err != nil || !reflect.DeepEqual(add, []string{LabelVerifyRequired}) || !reflect.DeepEqual(remove, []string{LabelVerifyOptional}) {
		t.Fatalf("plan = %v %v %v", add, remove, err)
	}
	add, remove, _ = VerificationLabelPlan(VerifyNone, []string{"bug", LabelVerifyOptional})
	if len(add) != 0 || len(remove) != 0 {
		t.Fatalf("already set: %v %v", add, remove)
	}
	add, remove, _ = VerificationLabelPlan(VerifyNone, []string{LabelVerifyRequired, LabelVerifyReceipt, "keep-me"})
	if !reflect.DeepEqual(add, []string{LabelVerifyOptional}) || !reflect.DeepEqual(remove, []string{LabelVerifyRequired, LabelVerifyReceipt}) {
		t.Fatalf("swap: %v %v", add, remove)
	}
}
