package ipc

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

const (
	maxBodyBytesTiny    int64 = 64 << 10
	maxBodyBytesSmall   int64 = 1 << 20
	maxBodyBytesCommand int64 = 8 << 20
)

func decodeJSONBody(w http.ResponseWriter, r *http.Request, dst any, maxBytes int64, allowEOF bool) (int, error) {
	if r == nil || r.Body == nil {
		if allowEOF {
			return 0, nil
		}
		return http.StatusBadRequest, fmt.Errorf("request body required")
	}
	if maxBytes > 0 {
		r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
	}
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(dst); err != nil {
		if allowEOF && errors.Is(err, io.EOF) {
			return 0, nil
		}
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			if maxBytes > 0 {
				return http.StatusRequestEntityTooLarge, fmt.Errorf("request body too large (max %d bytes)", maxBytes)
			}
			return http.StatusRequestEntityTooLarge, fmt.Errorf("request body too large")
		}
		return http.StatusBadRequest, err
	}
	return 0, nil
}
