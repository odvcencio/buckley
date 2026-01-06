# Smart Diff Abridging

## Problem
Tool execution was showing massive parameter values (hundreds of lines) in conversation, causing:
- Response truncation when hitting token limits
- Poor UX with overwhelming output
- Wasted tokens on redundant information

## Solution

### 1. **Parameter Abridging** (`pkg/ui/model.go:875-891`)

When displaying tool calls, long parameter values are now automatically abridged:

```go
const maxParamLen = 100
if len(str) > maxParamLen {
    // Count lines if it's multiline
    lines := strings.Split(str, "\n")
    if len(lines) > 3 {
        displayValue = fmt.Sprintf("[%d lines]", len(lines))
    } else {
        displayValue = str[:maxParamLen] + "..."
    }
}
```

**Before:**
```
● Iteration 25: Executing 1 tool(s)
  ⎿  search_replace (path: "docs/file.md", search: "# Very long content\n...[500 more lines]...", replace: "# New content\n...[500 more lines]...")
```

**After:**
```
● Iteration 25: Executing 1 tool(s)
  ⎿  search_replace (path: "docs/file.md", search: "[156 lines]", replace: "[163 lines]")
```

### 2. **Result Abridging** (`pkg/tool/builtin/types.go`)

Added `ShouldAbridge` and `DisplayData` fields to tool results:

```go
type Result struct {
    Success       bool
    Data          map[string]interface{} // Full data (always)
    DisplayData   map[string]interface{} // Abridged for conversation
    ShouldAbridge bool                   // Whether to abridge
}
```

### 3. **Smart Tool Responses**

**read_file**: Shows first 100 lines for large files
```
Read config.go (523 lines, 12KB)
... (423 more lines, 523 total)
```

**write_file**: Shows compact summary
```
✓ Created validator.go (234 lines, 8,912 bytes)
```

**search_replace**: Shows modification summary
```
✎ Modified executor.go: 3 replacement(s), 456→459 lines
```

**search_text**: Limits to first 50 matches
```
Found 127 matches (showing first 50)
```

### 4. **Diff Summarization** (`pkg/diff/summarizer.go`)

Comprehensive diff analysis utilities:
- Line-by-line change detection
- Function/method change detection (Go, JS/TS, Python)
- Batch file summaries
- Compact formatting

```go
✎ executor.go +15/-3 lines (executeTask, handleError, verify)
+ validator.go (new file, 234 lines)
✗ old_file.go (deleted, 45 lines)
```

## Benefits

✅ **Prevents truncation** - No more hitting token limits on large operations
✅ **Better UX** - Users see actionable summaries, not walls of text
✅ **Full functionality** - Models still get complete data in Data field
✅ **Token efficient** - Saves thousands of tokens per large operation
✅ **Automatic** - No configuration needed, works for all tools

## Configuration

All limits are tunable constants:
- `maxParamLen = 100` - Max chars before abridging parameters
- `maxDisplayLines = 100` - Max lines shown for file reads
- `maxDisplayMatches = 50` - Max search matches shown

## Implementation Files

- `pkg/ui/model.go` - Parameter display abridging
- `pkg/tool/builtin/types.go` - Result structure
- `pkg/tool/builtin/file.go` - File operation abridging
- `pkg/tool/builtin/search.go` - Search operation abridging
- `pkg/diff/summarizer.go` - Diff analysis utilities
