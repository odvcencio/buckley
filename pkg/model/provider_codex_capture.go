package model

import "bytes"

const (
	maxCodexStdoutBytes     = 16 * 1024 * 1024
	maxCodexStderrBytes     = 64 * 1024
	maxCodexDiagnosticBytes = 8 * 1024
)

type codexCaptureBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (b *codexCaptureBuffer) Write(data []byte) (int, error) {
	keep := min(len(data), max(0, b.limit-b.buf.Len()))
	_, _ = b.buf.Write(data[:keep])
	b.truncated = b.truncated || keep < len(data)
	return len(data), nil
}

type codexLineWriter struct {
	all           codexCaptureBuffer
	pending       []byte
	onLine        func([]byte)
	discardLine   bool
	lineTruncated bool
}

func (w *codexLineWriter) Write(data []byte) (int, error) {
	n, _ := w.all.Write(data)
	if w.onLine == nil {
		return n, nil
	}
	for len(data) > 0 {
		index := bytes.IndexByte(data, '\n')
		fragment := data
		if index >= 0 {
			fragment = data[:index]
		}
		if !w.discardLine {
			if len(w.pending)+len(fragment) > maxCodexStdoutBytes {
				w.pending = nil
				w.discardLine = true
				w.lineTruncated = true
			} else {
				w.pending = append(w.pending, fragment...)
			}
		}
		if index < 0 {
			break
		}
		if !w.discardLine {
			w.onLine(bytes.Clone(w.pending))
		}
		w.discardLine = false
		if cap(w.pending) > 64*1024 {
			w.pending = nil
		} else {
			w.pending = w.pending[:0]
		}
		data = data[index+1:]
	}
	return n, nil
}

func (w *codexLineWriter) Flush() {
	if len(w.pending) > 0 && !w.discardLine && w.onLine != nil {
		w.onLine(bytes.Clone(w.pending))
	}
	w.pending = nil
}

func boundedCodexDiagnostic(text string) string {
	if len(text) <= maxCodexDiagnosticBytes {
		return text
	}
	const marker = "\n... diagnostic truncated ...\n"
	half := (maxCodexDiagnosticBytes - len(marker)) / 2
	return text[:half] + marker + text[len(text)-half:]
}
