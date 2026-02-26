package image

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
)

const DefaultModel = "google/gemini-3-pro-image-preview"

// imageRequest is the OpenRouter request with response_modalities support.
type imageRequest struct {
	Model              string    `json:"model"`
	Messages           []message `json:"messages"`
	ResponseModalities []string  `json:"response_modalities"`
	MaxTokens          int       `json:"max_tokens,omitempty"`
}

type message struct {
	Role    string `json:"role"`
	Content any    `json:"content"`
}

type contentPart struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *imageURL `json:"image_url,omitempty"`
}

type imageURL struct {
	URL string `json:"url"`
}

// imageResponse is the OpenRouter chat completion response.
type imageResponse struct {
	ID      string        `json:"id"`
	Model   string        `json:"model"`
	Choices []imageChoice `json:"choices"`
	Usage   imageUsage    `json:"usage"`
}

type imageChoice struct {
	Index        int         `json:"index"`
	Message      responseMsg `json:"message"`
	FinishReason string      `json:"finish_reason"`
}

type responseMsg struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type imageUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// extractContent parses the response, returning text and decoded image bytes.
// Returns an error if no image data is found.
func (r *imageResponse) extractContent() (text string, imageData []byte, err error) {
	if len(r.Choices) == 0 {
		return "", nil, fmt.Errorf("empty response: no choices")
	}

	raw := r.Choices[0].Message.Content

	// Try as string first (text-only response, no image)
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return "", nil, fmt.Errorf("model did not generate an image")
	}

	// Parse as array of content parts
	var parts []contentPart
	if err := json.Unmarshal(raw, &parts); err != nil {
		return "", nil, fmt.Errorf("parsing content parts: %w", err)
	}

	for _, part := range parts {
		switch part.Type {
		case "text":
			text = part.Text
		case "image_url":
			if part.ImageURL == nil {
				continue
			}
			imageData, err = decodeDataURI(part.ImageURL.URL)
			if err != nil {
				return "", nil, fmt.Errorf("decoding image data: %w", err)
			}
		}
	}

	if imageData == nil {
		return "", nil, fmt.Errorf("model did not generate an image")
	}
	return text, imageData, nil
}

// decodeDataURI extracts base64 bytes from a data: URI.
func decodeDataURI(uri string) ([]byte, error) {
	idx := strings.Index(uri, ",")
	if idx < 0 {
		return nil, fmt.Errorf("invalid data URI: no comma separator")
	}
	return base64.StdEncoding.DecodeString(uri[idx+1:])
}

// buildRequest constructs the OpenRouter request for image generation.
func buildRequest(prompt string, size string, inputImage []byte, modelID string) imageRequest {
	if modelID == "" {
		modelID = DefaultModel
	}

	promptText := prompt
	if size != "" {
		promptText = fmt.Sprintf("%s\n\nImage dimensions: %s", prompt, size)
	}

	var parts []contentPart
	parts = append(parts, contentPart{Type: "text", Text: promptText})

	if inputImage != nil {
		encoded := base64.StdEncoding.EncodeToString(inputImage)
		parts = append(parts, contentPart{
			Type:     "image_url",
			ImageURL: &imageURL{URL: "data:image/png;base64," + encoded},
		})
	}

	return imageRequest{
		Model: modelID,
		Messages: []message{
			{Role: "user", Content: parts},
		},
		ResponseModalities: []string{"TEXT", "IMAGE"},
		MaxTokens:          4096,
	}
}
