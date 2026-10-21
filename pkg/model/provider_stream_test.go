package model

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestProviderStream_FrameBoundaries(t *testing.T) {
	for _, newline := range []string{"", "\n"} {
		for _, size := range []int{MaxProviderStreamFrameBytes - 1, MaxProviderStreamFrameBytes, MaxProviderStreamFrameBytes + 1} {
			t.Run(fmt.Sprintf("size_%d_newline_%t", size, newline != ""), func(t *testing.T) {
				scanner := newProviderStreamScanner(strings.NewReader(strings.Repeat("x", size) + newline))
				accepted := scanner.Scan()
				if size <= MaxProviderStreamFrameBytes {
					if !accepted || len(scanner.Bytes()) != size || scanner.Scan() || scanner.Err() != nil {
						t.Fatalf("exact frame rejected: accepted=%t error=%v", accepted, scanner.Err())
					}
				} else if accepted || !errors.Is(scanner.Err(), ErrProviderStreamFrameTooLarge) {
					t.Fatalf("oversized frame accepted: accepted=%t error=%v", accepted, scanner.Err())
				}
			})
		}
	}
}

func TestProviderStream_TotalBoundariesAndBufferedDone(t *testing.T) {
	for _, extra := range []int{0, 1} {
		t.Run(fmt.Sprint(extra), func(t *testing.T) {
			ending := "\ndata: [DONE]\n"
			comments := &repeatedStreamFrame{frame: ":" + strings.Repeat("x", (64<<10)-2) + "\n", remaining: int64(MaxProviderStreamBytes + extra - len(ending))}
			chunks := make(chan StreamChunk, 1)
			events, err := ParseSSEStreamWithEventCount(context.Background(), io.MultiReader(comments, strings.NewReader(ending)), chunks)
			if events != 0 || (extra == 0 && err != nil) || (extra > 0 && !errors.Is(err, ErrProviderStreamTooLarge)) {
				t.Fatalf("total boundary or buffered DONE changed: events=%d err=%v", events, err)
			}
		})
	}
}

func TestProviderStream_BudgetErrorsNeverRetry(t *testing.T) {
	for _, mode := range []string{"openrouter", "transport", "raw", "ollama"} {
		for _, limit := range []string{"frame", "total"} {
			if mode == "raw" && limit == "frame" {
				continue // Raw bodies have no record format until the caller parses them.
			}
			for _, partial := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/partial_%t", mode, limit, partial), func(t *testing.T) {
					prefix := ""
					if partial {
						prefix = "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n"
						if mode == "ollama" {
							prefix = "{\"message\":{\"content\":\"partial\"}}\n"
						}
					}
					var input io.Reader
					wantErr := ErrProviderStreamFrameTooLarge
					if limit == "frame" {
						input = io.MultiReader(strings.NewReader(prefix), &networkLogFlood{remaining: MaxProviderStreamFrameBytes + 1})
					} else {
						wantErr = ErrProviderStreamTooLarge
						frame := ":" + strings.Repeat("x", (64<<10)-2) + "\n"
						if mode == "ollama" {
							frame = strings.Repeat(" ", (64<<10)-1) + "\n"
						}
						input = io.MultiReader(strings.NewReader(prefix), &repeatedStreamFrame{frame: frame, remaining: MaxProviderStreamBytes + 1})
					}
					body := &trackedProviderErrorBody{Reader: input}
					requests := 0
					httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
						requests++
						return &http.Response{StatusCode: 200, ContentLength: -1, Header: make(http.Header), Body: body}, nil
					})}
					config := RetryConfig{MaxRetries: 2}
					chunks := make(chan StreamChunk, 10)
					var err error
					switch mode {
					case "raw":
						var raw io.ReadCloser
						raw, err = NewProviderTransport(ProviderTransportOptions{RetryConfig: &config}).DoStream(context.Background(), httpClient, "POST", "http://fixture.invalid", nil, nil)
						if raw != nil {
							_, err = io.Copy(io.Discard, raw)
							_ = raw.Close()
						}
					case "transport":
						err = NewProviderTransport(ProviderTransportOptions{RetryConfig: &config}).Stream(context.Background(), httpClient, "POST", "http://fixture.invalid", nil, nil, chunks)
					case "openrouter", "ollama":
						var incoming <-chan StreamChunk
						var errs <-chan error
						if mode == "openrouter" {
							client := NewClientWithOptions("test", "http://fixture.invalid", ClientOptions{RetryConfig: &config})
							client.httpClient = httpClient
							t.Cleanup(func() { _ = client.Close() })
							incoming, errs = client.ChatCompletionStream(context.Background(), ChatRequest{Model: "fixture"})
						} else {
							provider := NewOllamaProvider("http://fixture.invalid", false)
							provider.httpClient = httpClient
							incoming, errs = provider.ChatCompletionStream(context.Background(), ChatRequest{Model: "fixture"})
						}
						for chunk := range incoming {
							chunks <- chunk
						}
						for e := range errs {
							err = errors.Join(err, e)
						}
					}
					if !errors.Is(err, wantErr) || requests != 1 || !body.closed || isRetryableError(err) {
						t.Fatalf("budget error retried or lost ownership: requests=%d closed=%t err=%v", requests, body.closed, err)
					}
					if mode != "raw" {
						want := 0
						if partial {
							want = 1
						}
						if len(chunks) != want {
							t.Fatalf("delivered=%d want=%d", len(chunks), want)
						}
						if partial && (<-chunks).Choices[0].Delta.Content != "partial" {
							t.Fatal("lost partial output")
						}
					}
				})
			}
		}
	}
}

func TestProviderStream_RawAdvertisedLimitAndReadErrors(t *testing.T) {
	for _, advertised := range []bool{false, true} {
		t.Run(fmt.Sprint(advertised), func(t *testing.T) {
			input := strings.NewReader("partial")
			body := &trackedProviderErrorBody{Reader: input, terminalErr: io.ErrUnexpectedEOF}
			requests := 0
			httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				requests++
				length := int64(-1)
				if advertised {
					length = MaxProviderStreamBytes + 1
				}
				return &http.Response{StatusCode: 200, ContentLength: length, Header: make(http.Header), Body: body}, nil
			})}
			reader, err := NewProviderTransport(ProviderTransportOptions{}).DoStream(context.Background(), httpClient, "POST", "http://fixture.invalid", nil, nil)
			if advertised {
				if reader != nil || !errors.Is(err, ErrProviderStreamTooLarge) || requests != 1 || !body.closed || input.Len() != len("partial") {
					t.Fatalf("advertised overflow not closed immediately: body=%+v err=%v", body, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			data, err := io.ReadAll(reader)
			_ = reader.Close()
			if string(data) != "partial" || !errors.Is(err, io.ErrUnexpectedEOF) || !body.closed {
				t.Fatalf("raw body lost bytes/error/Close: %q %v", data, err)
			}
		})
	}
}

func TestProviderStream_OllamaCancellationAndReadError(t *testing.T) {
	for _, canceled := range []bool{false, true} {
		t.Run(fmt.Sprint(canceled), func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if canceled {
				cancel()
			}
			body := &trackedProviderErrorBody{Reader: strings.NewReader("{\"message\":{\"content\":\"partial\"}}\n"), terminalErr: io.ErrClosedPipe}
			provider := NewOllamaProvider("http://fixture.invalid", false)
			provider.httpClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: body}, nil
			})}
			chunks := make(chan StreamChunk, 2)
			err := provider.invokeStream(ctx, ChatRequest{Model: "fixture"}, chunks)
			want := io.ErrClosedPipe
			wantEvents := 1
			if canceled {
				want = context.Canceled
				wantEvents = 0
			}
			if !errors.Is(err, want) || !body.closed || len(chunks) != wantEvents {
				t.Fatalf("lost cancellation/read failure: events=%d closed=%t err=%v", len(chunks), body.closed, err)
			}
		})
	}
}

func TestProviderStream_OllamaCompletionContract(t *testing.T) {
	for _, tc := range []struct {
		name, wire string
		wantError  bool
		events     int
	}{
		{"done_without_newline", `{"message":{"content":"ok"},"done":true,"prompt_eval_count":7,"eval_count":5}`, false, 1},
		{"done_stops_reading", "{\"done\":true}\nnot JSON\n", false, 1},
		{"missing_done", "{\"message\":{\"content\":\"partial\"}}\n", true, 1},
		{"invalid_json", "{\"message\":{\"content\":\"partial\"}}\nnot JSON\n", true, 1},
		{"empty", "", true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := &trackedProviderErrorBody{Reader: strings.NewReader(tc.wire)}
			provider := NewOllamaProvider("http://fixture.invalid", false)
			provider.httpClient = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: 200, Header: make(http.Header), Body: body}, nil
			})}
			chunks := make(chan StreamChunk, 10)
			err := provider.invokeStream(context.Background(), ChatRequest{Model: "fixture"}, chunks)
			if (err != nil) != tc.wantError || len(chunks) != tc.events || !body.closed {
				t.Fatalf("completion changed: events=%d closed=%t err=%v", len(chunks), body.closed, err)
			}
			if tc.name == "done_without_newline" {
				chunk := <-chunks
				if chunk.Choices[0].Delta.Content != "ok" || chunk.Usage == nil || chunk.Usage.PromptTokens != 7 || chunk.Usage.CompletionTokens != 5 || chunk.Choices[0].FinishReason == nil {
					t.Fatalf("lost final content/usage: %+v", chunk)
				}
			}
			if tc.name == "missing_done" && !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatal("truncated stream reported as complete")
			}
		})
	}
}
