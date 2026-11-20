package main

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"m31labs.dev/buckley/pkg/agentloop"
	"m31labs.dev/buckley/pkg/model"
)

func TestParseStartupOptionsContextFiles(t *testing.T) {
	opts, err := parseStartupOptions([]string{"--context-file", "a.md", "--context-file=b.md", "-p", "question"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(opts.contextFiles, ",") != "a.md,b.md" {
		t.Fatalf("context files = %v, want a.md,b.md in order", opts.contextFiles)
	}
	if _, err := parseStartupOptions([]string{"--context-file"}); err == nil || !strings.Contains(err.Error(), "--context-file requires a path") {
		t.Fatalf("missing path error = %v", err)
	}
}

// contextText joins the text of context parts.
func contextText(parts []model.ContentPart) string {
	var b strings.Builder
	for _, part := range parts {
		b.WriteString(part.Text)
	}
	return b.String()
}

func TestOneShotContextFilesFormTheSamePrefixOnEveryRun(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "spec.md"), []byte("# Spec\nrule one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("no trailing newline"), 0o600); err != nil {
		t.Fatal(err)
	}
	paths := []string{"spec.md", "notes.txt"}
	first, err := loadOneShotContextFiles(dir, paths)
	if err != nil {
		t.Fatal(err)
	}
	second, err := loadOneShotContextFiles(dir, paths)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatalf("two runs with the same files differ:\n%#v\n%#v", first, second)
	}
	if len(first) != 1 || !first[0].CacheBreakpoint {
		t.Fatalf("text files = %#v, want one text part marked as the stable prefix", first)
	}
	text := contextText(first)
	if strings.Index(text, `path="spec.md"`) > strings.Index(text, `path="notes.txt"`) {
		t.Fatal("files are not in the order given")
	}
	if !strings.Contains(text, "no trailing newline\n</context_file>") {
		t.Fatalf("file end not closed on its own line:\n%s", text)
	}

	// Two questions about the same files: the marked context part is the
	// same, and only the question after it changes.
	a := oneShotContextMessage(first, "What is rule one?").Content.([]model.ContentPart)
	b := oneShotContextMessage(second, "Is there a rule two?").Content.([]model.ContentPart)
	if len(a) != 2 || !a[0].CacheBreakpoint || a[1].CacheBreakpoint {
		t.Fatalf("parts = %#v, want the context marked as the stable prefix, then the question", a)
	}
	if a[0] != b[0] || a[1].Text == b[1].Text {
		t.Fatal("the stable prefix differs between runs")
	}

	if _, err := loadOneShotContextFiles(dir, []string{"missing.md"}); err == nil || !strings.Contains(err.Error(), "missing.md") {
		t.Fatalf("missing file error = %v", err)
	}
	if parts, err := loadOneShotContextFiles(dir, nil); err != nil || parts != nil {
		t.Fatalf("no files = %#v, %v; want none", parts, err)
	}
}

// testPNG encodes a small solid PNG.
func testPNG(t *testing.T, shade uint8) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 4, 3))
	for i := range img.Pix {
		img.Pix[i] = shade
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestOneShotContextFilesCarryImagesInOrder(t *testing.T) {
	dir := t.TempDir()
	files := map[string][]byte{
		"title.png": testPNG(t, 10),
		// A WebP header is enough for content sniffing.
		"menu.webp": append([]byte("RIFF\x00\x00\x00\x00WEBPVP8 "), make([]byte, 16)...),
		// An image without an image extension is found by its bytes.
		"capture":  testPNG(t, 200),
		"notes.md": []byte("menu notes\n"),
	}
	for name, data := range files {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	paths := []string{"title.png", "notes.md", "menu.webp", "capture"}
	first, err := loadOneShotContextFiles(dir, paths)
	if err != nil {
		t.Fatal(err)
	}
	second, err := loadOneShotContextFiles(dir, paths)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) {
		t.Fatal("two runs with the same images differ")
	}
	var images []string
	for _, part := range first {
		if part.ImageURL != nil {
			images = append(images, part.ImageURL.URL[:strings.Index(part.ImageURL.URL, ",")])
		}
	}
	want := []string{"data:image/png;base64", "data:image/webp;base64", "data:image/png;base64"}
	if !reflect.DeepEqual(images, want) {
		t.Fatalf("images = %v, want %v in the order given", images, want)
	}
	if got := first[0].Text; !strings.Contains(got, `<context_image path="title.png" type="image/png">`) {
		t.Fatalf("first part = %q, want the label before the first image", got)
	}
	last := first[len(first)-1]
	if !last.CacheBreakpoint || last.ImageURL != nil || last.Text == "" {
		t.Fatalf("last part = %#v, want text marked as the end of the stable prefix", last)
	}
	for _, part := range first[:len(first)-1] {
		if part.CacheBreakpoint {
			t.Fatal("a part before the end is marked as the end of the prefix")
		}
		if part.ImageURL == nil && part.Text == "" {
			t.Fatal("an empty text part would be rejected by Anthropic")
		}
	}
	if !strings.Contains(contextText(first), "menu notes") {
		t.Fatal("the text file between images is missing")
	}
	data, _ := base64.StdEncoding.DecodeString(strings.SplitN(first[1].ImageURL.URL, ",", 2)[1])
	if !bytes.Equal(data, files["title.png"]) {
		t.Fatal("the first image's bytes changed")
	}
}

func TestOneShotContextFilesRejectBadImages(t *testing.T) {
	dir := t.TempDir()
	big := append(testPNG(t, 1), make([]byte, maxContextImageBytes)...)
	gif := []byte("GIF89a\x01\x00\x01\x00\x00\x00\x00;")
	for name, data := range map[string][]byte{
		"huge.png":  big,
		"fake.png":  []byte("plain text with an image name"),
		"anim.gif":  gif,
		"blob.bin":  {0xff, 0xfe, 0x00, 0x01, 0x80},
		"photo.jpg": {0x00, 0x01, 0x02},
	} {
		if err := os.WriteFile(filepath.Join(dir, name), data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for name, want := range map[string]string{
		"huge.png":  "the limit is 3.75 MB per image",
		"fake.png":  "is named as an image but is not a PNG, JPEG or WebP image",
		"anim.gif":  "is image/gif; images must be PNG, JPEG or WebP",
		"blob.bin":  "neither UTF-8 text nor a PNG, JPEG or WebP image",
		"photo.jpg": "is named as an image",
	} {
		_, err := loadOneShotContextFiles(dir, []string{name})
		if err == nil || !strings.Contains(err.Error(), "--context-file "+name) || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: error = %v, want the file named and %q", name, err, want)
		}
	}
}

func TestOneShotContextPromptCacheIsOnWithoutABudget(t *testing.T) {
	var notes bytes.Buffer
	if cache := oneShotContextPromptCache(nil, "anthropic/claude-fable-5-2", 0, &notes); cache == nil || !cache.Enabled {
		t.Fatalf("uncapped run: cache = %#v, want on", cache)
	}
	if notes.Len() != 0 {
		t.Fatalf("uncapped run printed %q", notes.String())
	}
	// A capped run on a model with no known cache prices runs uncached and
	// says which prices are missing; it is not refused.
	if cache := oneShotContextPromptCache(nil, "vendor/model", 0.5, &notes); cache != nil {
		t.Fatalf("capped run without cache prices: cache = %#v, want off", cache)
	}
	if !strings.Contains(notes.String(), "cache_read_per_million") {
		t.Fatalf("note = %q, want the config key", notes.String())
	}
}

func TestOneShotUsageLineIsPrintedWithoutABudget(t *testing.T) {
	result := &agentloop.Result{ModelRequests: 1}
	result.Usage = model.Usage{
		PromptTokens:        9000,
		CompletionTokens:    100,
		PromptTokensDetails: &model.PromptTokensDetails{CachedTokens: 8000},
		CacheWriteTokens:    500,
	}
	line := oneShotUsageLine(nil, "vendor/model", result, 0)
	for _, want := range []string{"Usage detail:", "cache_read_tokens=8000", "cache_write_tokens=500", "estimated_cost_usd=unknown"} {
		if !strings.Contains(line, want) {
			t.Fatalf("line %q lacks %q", line, want)
		}
	}
	if strings.Contains(line, "Budgeted run") {
		t.Fatalf("uncapped run printed a budget line: %q", line)
	}
	if !strings.Contains(oneShotUsageLine(nil, "vendor/model", result, 0.5), "ceiling_usd=0.500000") {
		t.Fatal("capped run lacks its budget line")
	}
}

func TestOneShotBillingNamesWhoPays(t *testing.T) {
	cost := func(v float64) *float64 { return &v }
	for _, tt := range []struct {
		provider, model string
		usage           model.Usage
		want            string
	}{
		{"anthropic", "anthropic/claude-fable-5-2", model.Usage{}, "billed_to=anthropic"},
		{"openai", "openai/gpt-6.2-astra", model.Usage{}, "billed_to=openai"},
		{"openrouter", "openrouter/google/gemini-4-neon", model.Usage{Cost: cost(0.0399)}, "billed_to=openrouter provider_cost_usd=0.039900"},
		{"openrouter", "openrouter/google/gemini-4-neon", model.Usage{}, "billed_to=openrouter provider_cost_usd=unknown"},
		{"openrouter", "openrouter/openai/gpt-6.2-sol", model.Usage{Cost: cost(0.0001), IsBYOK: true, CostDetails: &model.UsageCostDetails{UpstreamInferenceCost: 0.0014}},
			"billed_to=openai(byok) upstream_cost_usd=0.001400 openrouter_fee_usd=0.000100"},
		{"openrouter", "openai/gpt-6.2-sol", model.Usage{IsBYOK: true}, "billed_to=openai(byok) upstream_cost_usd=unknown openrouter_fee_usd=unknown"},
	} {
		if got := oneShotBilling(tt.provider, tt.model, tt.usage); got != tt.want {
			t.Errorf("%s %s: %q, want %q", tt.provider, tt.model, got, tt.want)
		}
	}
}
