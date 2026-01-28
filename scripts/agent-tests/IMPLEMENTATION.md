# Self-Healing E2E Test Implementation

## Overview

This directory contains a **production-ready** self-healing E2E testing framework for Buckley's TUI. Unlike traditional E2E tests that break when UI changes, this system uses AI-inspired semantic matching to adapt to UI modifications.

## Key Innovation: Semantic Matching

Traditional E2E tests use brittle selectors:
```javascript
// Traditional - breaks when button text changes
click("#send-btn")  
click("button[data-testid='submit']")
```

Self-healing tests use semantic intent:
```go
// Self-healing - adapts to UI changes
FindWidgetSemantic(snap, MatchCriteria{
    Keywords: []string{"send", "submit", "go"},
    Role:     "button",
})
```

## How It Works

### 1. UI Snapshot Capture
The driver connects to Buckley's agent socket and captures:
- Widget hierarchy with roles (button, textbox, list, etc.)
- Labels, descriptions, and values
- Spatial information (bounds, positions)
- Focus state

### 2. Multi-Strategy Matching
When looking for a widget, the matcher tries (in order):

| Strategy | Use Case | Confidence |
|----------|----------|------------|
| Exact | No UI changes | 1.0 |
| Fuzzy | Typos, minor text changes | 0.6-0.9 |
| Keyword | Multiple acceptable labels | 0.4-0.8 |
| Spatial | "button at bottom" | 0.6-0.9 |

### 3. Goal-Based Parsing
Human-readable goals are parsed into match criteria:
```go
// "Focus the input area"
parsed = {
    Role: "textbox",
    Keywords: ["input", "text", "message"],
    PreferredArea: "bottom"
}

// "Click the send button"
parsed = {
    Role: "button", 
    Label: "send",
    Keywords: ["send", "submit"]
}
```

## Self-Healing in Action

### Scenario 1: Button Label Change
```
Before: Button label = "Send"
After:  Button label = "Submit →"

Traditional test: ❌ FAILS (selector "#send" not found)
Self-healing test: ✅ PASSES (matches keyword "submit")
```

### Scenario 2: Layout Reorganization
```
Before: Input at bottom of screen
After:  Input moved to sidebar

Traditional test: ❌ FAILS (coordinates changed)
Self-healing test: ✅ PASSES (finds by role "textbox")
```

### Scenario 3: Component Refactoring
```
Before: <div id="chat-input">
After:  <textarea class="message-field">

Traditional test: ❌ FAILS (ID changed)
Self-healing test: ✅ PASSES (semantic role matching)
```

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                    Test Scenario (JSON)                      │
│  {                                                           │
│    "goal": "Focus the input area",  ← Human intent          │
│    "action": "focus"                                         │
│  }                                                           │
└───────────────────────┬─────────────────────────────────────┘
                        │
                        ▼
┌─────────────────────────────────────────────────────────────┐
│                   Goal Parser                                │
│  "Focus the input area" → {Role: "textbox", Area: "bottom"} │
└───────────────────────┬─────────────────────────────────────┘
                        │
                        ▼
┌─────────────────────────────────────────────────────────────┐
│                Semantic Matcher                              │
│  1. Try exact match                                         │
│  2. Try fuzzy match (Levenshtein)                           │
│  3. Try keyword matching                                    │
│  4. Try spatial heuristics                                  │
└───────────────────────┬─────────────────────────────────────┘
                        │
                        ▼
┌─────────────────────────────────────────────────────────────┐
│                   Agent Driver                               │
│  - Connects to Buckley via Unix socket                      │
│  - Captures UI snapshots                                    │
│  - Executes actions (click, type, key)                      │
└───────────────────────┬─────────────────────────────────────┘
                        │
                        ▼
┌─────────────────────────────────────────────────────────────┐
│               fluffy-ui Agent Socket                         │
│              --agent-socket unix:/tmp/buckley.sock          │
└─────────────────────────────────────────────────────────────┘
```

## Running the Tests

### Prerequisites
```bash
# Build Buckley with agent socket support
cd /home/draco/work/buckley
make build

# Terminal 1: Start Buckley with agent socket
./buckley --agent-socket unix:/tmp/buckley.sock
```

### Run Tests
```bash
# List available scenarios
make agent-test-list

# Run smoke test
make agent-test-smoke

# Run all tests
make agent-test-all

# Interactive demo mode
make agent-test-demo
```

### Custom Scenarios
Create a JSON scenario file:
```json
{
  "name": "My Custom Flow",
  "steps": [
    {
      "goal": "Focus the input area",
      "action": "focus",
      "target": "input"
    },
    {
      "goal": "Type a message",
      "action": "type",
      "text": "Hello world"
    },
    {
      "goal": "Submit the form",
      "action": "key",
      "key": "enter"
    }
  ]
}
```

Run it:
```bash
./scripts/agent-tests/runner.sh --scenario my-scenario.json --verbose
```

## Confidence Thresholds

The matcher uses adaptive confidence thresholds:
- **0.8+**: High confidence, exact or near-exact match
- **0.6-0.8**: Good match, acceptable for most actions
- **0.4-0.6**: Low confidence, requires confirmation in strict mode
- **<0.4**: Reject, manual intervention required

## Extending the Framework

### Add New Matching Strategy
```go
// In matcher.go
func matchMyStrategy(widgets []WidgetInfo, criteria MatchCriteria) *MatchResult {
    // Your custom logic
    return &MatchResult{
        Widget:     bestMatch,
        Confidence: confidenceScore,
        Strategy:   "my_strategy",
    }
}
```

### Add New Action Type
```go
// In driver.go runScenario()
case "my_action":
    if err := d.MyAction(step.Param); err != nil {
        return fmt.Errorf("step %d: %w", i+1, err)
    }
```

## Metrics and Debugging

Enable verbose mode to see:
- Which matching strategy was used
- Confidence scores for each attempt
- Full widget snapshots on failure

```bash
./scripts/agent-tests/runner.sh --scenario smoke.json --verbose
```

## Future Enhancements

1. **Visual Diff**: Compare screenshots for visual regression
2. **LLM Integration**: Use LLM for complex semantic understanding
3. **Learning Mode**: Record manual interactions to generate scenarios
4. **CI Integration**: GitHub Actions workflow for automated testing
