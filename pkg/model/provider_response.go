package model

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// MaxProviderResponseBytes bounds ordinary non-streaming JSON response reads.
const MaxProviderResponseBytes = 32 << 20

// ErrProviderResponseTooLarge is terminal: repeating the request can bill again.
var ErrProviderResponseTooLarge = fmt.Errorf("model: provider response exceeds %d-byte limit", MaxProviderResponseBytes)

type providerResponseReader struct {
	source    io.Reader
	remaining int64
}

func (r *providerResponseReader) Read(dst []byte) (int, error) {
	if r.remaining < 0 {
		return 0, ErrProviderResponseTooLarge
	}
	if len(dst) == 0 {
		return 0, nil
	}
	dst = dst[:min(int64(len(dst)), r.remaining+1)]
	n, err := r.source.Read(dst)
	if int64(n) > r.remaining {
		n = int(r.remaining)
		r.remaining = -1
		return n, ErrProviderResponseTooLarge
	}
	r.remaining -= int64(n)
	return n, err
}

func newProviderResponseReader(response *http.Response) (*providerResponseReader, error) {
	if response.ContentLength > MaxProviderResponseBytes {
		return nil, ErrProviderResponseTooLarge
	}
	return &providerResponseReader{source: response.Body, remaining: MaxProviderResponseBytes}, nil
}

func decodeProviderResponse(response *http.Response, target any) error {
	reader, err := newProviderResponseReader(response)
	if err != nil {
		return err
	}
	err = json.NewDecoder(reader).Decode(target)
	// A decoder can finish a value before observing an error returned with bytes.
	if reader.remaining < 0 {
		return ErrProviderResponseTooLarge
	}
	return err
}

func readProviderResponse(response *http.Response) ([]byte, error) {
	reader, err := newProviderResponseReader(response)
	if err != nil {
		return nil, err
	}
	data, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	return data, nil
}
