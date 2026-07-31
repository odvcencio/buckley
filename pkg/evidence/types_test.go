package evidence

import "testing"

func TestComputeContentID_Deterministic(t *testing.T) {
	content := []byte("package main\n\nfunc main() {}\n")

	id1 := ComputeContentID(KindSource, "text/x-go", content)
	id2 := ComputeContentID(KindSource, "text/x-go", content)

	if id1 != id2 {
		t.Fatalf("ComputeContentID not deterministic: %q != %q", id1, id2)
	}
	if id1[:3] != "ev_" {
		t.Fatalf("ComputeContentID missing ev_ prefix: %q", id1)
	}
	if len(id1) != len("ev_")+26 {
		t.Fatalf("ComputeContentID length = %d, want %d", len(id1), len("ev_")+26)
	}
}

func TestComputeContentID_DiffersByContent(t *testing.T) {
	a := ComputeContentID(KindSource, "text/x-go", []byte("a"))
	b := ComputeContentID(KindSource, "text/x-go", []byte("b"))
	if a == b {
		t.Fatalf("expected different IDs for different content, both = %q", a)
	}
}

func TestComputeContentID_DiffersByKind(t *testing.T) {
	content := []byte("same bytes")
	a := ComputeContentID(KindSource, "text/plain", content)
	b := ComputeContentID(KindDiff, "text/plain", content)
	if a == b {
		t.Fatalf("expected different IDs for different kinds, both = %q", a)
	}
}

func TestComputeContentID_NoFieldBoundaryCollision(t *testing.T) {
	// Without a separator between kind and media_type, ("ab","c",x) and
	// ("a","bc",x) would hash identically. This guards the resolution
	// documented on ComputeContentID.
	a := ComputeContentID(Kind("ab"), "c", []byte("x"))
	b := ComputeContentID(Kind("a"), "bc", []byte("x"))
	if a == b {
		t.Fatalf("expected field-boundary collision to be avoided, both = %q", a)
	}
}

func TestContentSHA256Hex(t *testing.T) {
	// Known SHA-256 of the empty string.
	const want = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	got := ContentSHA256Hex(nil)
	if got != want {
		t.Fatalf("ContentSHA256Hex(nil) = %q, want %q", got, want)
	}
	if len(got) != 64 {
		t.Fatalf("ContentSHA256Hex(nil) length = %d, want 64", len(got))
	}
}
