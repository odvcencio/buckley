package transparency

import (
	"testing"
)

func TestContextAudit(t *testing.T) {
	audit := NewContextAudit()

	audit.Add("source_a", 100)
	audit.Add("source_b", 200)
	audit.Add("source_c", 50)

	// Test total
	if total := audit.TotalTokens(); total != 350 {
		t.Errorf("expected total 350, got %d", total)
	}

	// Test sources sorted by token count
	sources := audit.Sources()
	if len(sources) != 3 {
		t.Errorf("expected 3 sources, got %d", len(sources))
	}
	if sources[0].Name != "source_b" {
		t.Errorf("expected first source 'source_b', got %q", sources[0].Name)
	}
	if sources[0].Tokens != 200 {
		t.Errorf("expected first source 200 tokens, got %d", sources[0].Tokens)
	}
}

func TestContextAuditTruncation(t *testing.T) {
	audit := NewContextAudit()

	audit.Add("normal", 100)
	audit.AddTruncated("truncated", 500, 1000)

	if !audit.HasTruncation() {
		t.Error("expected HasTruncation to return true")
	}

	sources := audit.Sources()
	var truncatedSource *ContextSource
	for i := range sources {
		if sources[i].Name == "truncated" {
			truncatedSource = &sources[i]
			break
		}
	}

	if truncatedSource == nil {
		t.Fatal("expected to find truncated source")
	}
	if !truncatedSource.Truncated {
		t.Error("expected Truncated flag to be true")
	}
	if truncatedSource.OriginalTokens != 1000 {
		t.Errorf("expected OriginalTokens 1000, got %d", truncatedSource.OriginalTokens)
	}
}

func TestContextSourcePercentage(t *testing.T) {
	source := ContextSource{Tokens: 25}

	pct := source.Percentage(100)
	if pct != 25.0 {
		t.Errorf("expected 25%%, got %.2f%%", pct)
	}

	// Test zero total
	pct = source.Percentage(0)
	if pct != 0 {
		t.Errorf("expected 0%% for zero total, got %.2f%%", pct)
	}
}

func TestContextAuditMerge(t *testing.T) {
	audit1 := NewContextAudit()
	audit1.Add("a", 100)

	audit2 := NewContextAudit()
	audit2.Add("b", 200)

	audit1.Merge(audit2)

	if total := audit1.TotalTokens(); total != 300 {
		t.Errorf("expected total 300 after merge, got %d", total)
	}

	sources := audit1.Sources()
	if len(sources) != 2 {
		t.Errorf("expected 2 sources after merge, got %d", len(sources))
	}
}

func TestContextAuditMergeNil(t *testing.T) {
	audit := NewContextAudit()
	audit.Add("a", 100)

	// Should not panic
	audit.Merge(nil)

	if total := audit.TotalTokens(); total != 100 {
		t.Errorf("expected total 100, got %d", total)
	}
}
