package model

import "testing"

func TestOpenAIProvider_GetModelInfo_KnownCuratedModel(t *testing.T) {
	p := NewOpenAIProvider("test-key", "", false)

	info, err := p.GetModelInfo("openai/gpt-4o")
	if err != nil {
		t.Fatalf("GetModelInfo(known model) returned error: %v", err)
	}
	if info == nil {
		t.Fatal("GetModelInfo(known model) returned nil info")
	}
	if info.ID != "openai/gpt-4o" {
		t.Errorf("info.ID = %q, want %q", info.ID, "openai/gpt-4o")
	}
	if info.Name != "GPT-4o" {
		t.Errorf("info.Name = %q, want curated name %q", info.Name, "GPT-4o")
	}
	if info.ContextLength != 128000 {
		t.Errorf("info.ContextLength = %d, want 128000", info.ContextLength)
	}
}

func TestOpenAIProvider_GetModelInfo_UnknownModernModel(t *testing.T) {
	p := NewOpenAIProvider("test-key", "", false)

	info, err := p.GetModelInfo("openai/gpt-5.6")
	if err != nil {
		t.Fatalf("GetModelInfo(unknown modern model) returned error: %v", err)
	}
	if info == nil {
		t.Fatal("GetModelInfo(unknown modern model) returned nil info")
	}
	if info.ID != "openai/gpt-5.6" {
		t.Errorf("info.ID = %q, want %q", info.ID, "openai/gpt-5.6")
	}
	if info.Name != "openai/gpt-5.6" {
		t.Errorf("info.Name = %q, want %q", info.Name, "openai/gpt-5.6")
	}
}

func TestOpenAIProvider_GetModelInfo_UnqualifiedCuratedModel(t *testing.T) {
	p := NewOpenAIProvider("test-key", "", false)

	info, err := p.GetModelInfo("gpt-4o")
	if err != nil {
		t.Fatalf("GetModelInfo(unqualified curated model) returned error: %v", err)
	}
	if info == nil {
		t.Fatal("GetModelInfo(unqualified curated model) returned nil info")
	}
	if info.ID != "openai/gpt-4o" {
		t.Errorf("info.ID = %q, want %q", info.ID, "openai/gpt-4o")
	}
	if info.Name != "GPT-4o" {
		t.Errorf("info.Name = %q, want curated name %q", info.Name)
	}
	if info.ContextLength != 128000 {
		t.Errorf("info.ContextLength = %d, want 128000", info.ContextLength)
	}
}

func TestOpenAIProvider_GetModelInfo_EmptyAndWhitespaceIDs(t *testing.T) {
	p := NewOpenAIProvider("test-key", "", false)

	for _, modelID := range []string{"", "   ", "\t\n"} {
		info, err := p.GetModelInfo(modelID)
		if err == nil {
			t.Errorf("GetModelInfo(%q) expected error, got nil", modelID)
		}
		if info != nil {
			t.Errorf("GetModelInfo(%q) expected nil info, got %+v", modelID, info)
		}
	}
}
