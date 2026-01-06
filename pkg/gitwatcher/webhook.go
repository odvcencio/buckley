package gitwatcher

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

type MergeCallback func(MergeEvent)

type MergeEvent struct {
	Repository string
	Branch     string
	SHA        string
}

type Handler struct {
	secret   string
	callback MergeCallback
}

func NewHandler(secret string, callback MergeCallback) *Handler {
	return &Handler{secret: strings.TrimSpace(secret), callback: callback}
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	payload, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	if h.secret != "" && !validateSignature(r.Header.Get("X-Hub-Signature-256"), payload, h.secret) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var event map[string]any
	if err := json.Unmarshal(payload, &event); err != nil {
		http.Error(w, "unprocessable entity", http.StatusUnprocessableEntity)
		return
	}
	go h.handleEvent(event)
	w.WriteHeader(http.StatusAccepted)
	_, _ = w.Write([]byte("ok"))
}

func (h *Handler) handleEvent(event map[string]any) {
	action := readString(event, "action")
	merged := readBool(event, "merged")
	if action == "closed" && merged && h.callback != nil {
		repo := readNestedString(event, "repository", "full_name")
		branch := readNestedString(event, "pull_request", "base", "ref")
		sha := readNestedString(event, "pull_request", "merge_commit_sha")
		if repo == "" || branch == "" {
			return
		}
		h.callback(MergeEvent{Repository: repo, Branch: branch, SHA: sha})
	}
}

func validateSignature(signature string, payload []byte, secret string) bool {
	if signature == "" {
		return false
	}
	const prefix = "sha256="
	if !strings.HasPrefix(signature, prefix) {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(payload)
	expected := mac.Sum(nil)
	sigBytes, err := hex.DecodeString(signature[len(prefix):])
	if err != nil {
		return false
	}
	return hmac.Equal(expected, sigBytes)
}

func readString(payload map[string]any, key string) string {
	if val, ok := payload[key].(string); ok {
		return val
	}
	return ""
}

func readBool(payload map[string]any, key string) bool {
	if val, ok := payload[key].(bool); ok {
		return val
	}
	return false
}

func readNestedString(payload map[string]any, keys ...string) string {
	current := payload
	for i, key := range keys {
		value, ok := current[key]
		if !ok {
			return ""
		}
		if i == len(keys)-1 {
			if s, ok := value.(string); ok {
				return s
			}
			return ""
		}
		next, ok := value.(map[string]any)
		if !ok {
			return ""
		}
		current = next
	}
	return ""
}
