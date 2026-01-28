package builtin

import (
	"sync"
)

// resultPool provides memory-efficient recycling of Result objects
// to reduce GC pressure during high-volume tool execution.
var resultPool = sync.Pool{
	New: func() any {
		return &Result{}
	},
}

// AcquireResult retrieves a Result from the pool.
// The Result is reset and ready for use.
func AcquireResult() *Result {
	r := resultPool.Get().(*Result)
	r.Reset()
	return r
}

// ReleaseResult returns a Result to the pool for reuse.
// The Result should not be used after this call.
func ReleaseResult(r *Result) {
	if r == nil {
		return
	}
	r.Reset()
	resultPool.Put(r)
}

// Reset clears the Result for reuse.
// It resets all fields while preserving the underlying map capacity
// to reduce future allocations.
func (r *Result) Reset() {
	if r == nil {
		return
	}

	r.Success = false
	r.Error = ""
	r.ShouldAbridge = false
	r.NeedsApproval = false
	r.ApprovalFunc = nil

	// Clear and reuse maps to preserve capacity
	if r.Data != nil {
		for k := range r.Data {
			delete(r.Data, k)
		}
	}
	if r.DisplayData != nil {
		for k := range r.DisplayData {
			delete(r.DisplayData, k)
		}
	}
	r.DiffPreview = nil
}

// resultSlicePool provides memory-efficient recycling of Result slices.
// This is useful for batch operations that return multiple results.
var resultSlicePool = sync.Pool{
	New: func() any {
		s := make([]*Result, 0, 8)
		return &s
	},
}

// AcquireResultSlice retrieves a Result slice from the pool.
func AcquireResultSlice() []*Result {
	s := resultSlicePool.Get().(*[]*Result)
	return (*s)[:0]
}

// ReleaseResultSlice returns a Result slice to the pool.
func ReleaseResultSlice(s []*Result) {
	if s == nil {
		return
	}
	// Only pool reasonably-sized slices to avoid memory bloat
	if cap(s) > 1024 {
		return
	}
	resultSlicePool.Put(&s)
}
