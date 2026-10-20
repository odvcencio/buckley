//go:build windows

package builtin

import "context"

// Windows retains the bounded file reader until a compatible streamed writer is available.
type testReportCapture struct {
	path  string
	limit int64
}

func newTestReportCapture(path string, limit int64) (*testReportCapture, error) {
	return &testReportCapture{path: path, limit: limit}, nil
}
func (c *testReportCapture) bytes(ctx context.Context) ([]byte, bool, error) {
	if err := ctx.Err(); err != nil {
		return nil, false, err
	}
	text, _, exceeded, err := readFileSnapshot(c.path, c.limit)
	return []byte(text), exceeded, err
}
func (c *testReportCapture) close() {}
