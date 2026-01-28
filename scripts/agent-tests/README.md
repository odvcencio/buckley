# Buckley Agent E2E Testing Framework

A self-healing end-to-end testing system that uses the fluffy-ui agent socket to interact with Buckley's TUI programmatically.

## Overview

Traditional E2E tests break when UI changes. This framework uses AI to:
1. **Understand** the UI state via accessibility snapshots
2. **Decide** what action to take based on semantic goals
3. **Execute** actions via the agent socket
4. **Adapt** when UI elements move or change

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                    Test Scenario (Go)                        │
│  Goal: "Send a message and verify response appears"         │
└───────────────────────┬─────────────────────────────────────┘
                        │
                        ▼
┌─────────────────────────────────────────────────────────────┐
│                  Agent Driver (Go)                           │
│  - Connects to Buckley via Unix socket                      │
│  - Takes snapshots of UI state                              │
│  - Uses semantic matching to find widgets                   │
│  - Executes actions (click, type, key)                      │
└───────────────────────┬─────────────────────────────────────┘
                        │
                        ▼
┌─────────────────────────────────────────────────────────────┐
│                   Buckley TUI                                │
│              --agent-socket unix:/tmp/buckley.sock          │
└─────────────────────────────────────────────────────────────┘
```

## Usage

```bash
# Terminal 1: Start Buckley with agent socket
buckley --agent-socket unix:/tmp/buckley.sock

# Terminal 2: Run self-healing tests
./scripts/agent-tests/runner \
  --socket unix:/tmp/buckley.sock \
  --scenario scenarios/smoke.json
```

## Test Scenarios

Scenarios are declarative JSON files describing goals, not specific UI elements:

```json
{
  "name": "Basic Chat Flow",
  "steps": [
    {
      "goal": "Focus the input area",
      "action": "focus",
      "target": "text input"
    },
    {
      "goal": "Type a test message",
      "action": "type",
      "text": "Hello, can you echo this back?"
    },
    {
      "goal": "Submit the message",
      "action": "key",
      "key": "enter"
    },
    {
      "goal": "Verify response appears",
      "action": "wait_for",
      "condition": "text_contains",
      "value": "echo"
    }
  ]
}
```

## Self-Healing Strategy

Instead of XPath/CSS selectors, the framework uses:

1. **Semantic matching**: Find widget by role + label similarity
2. **Context awareness**: Use surrounding widgets as anchors
3. **Fallback chains**: Try multiple strategies before failing
4. **Visual verification**: Compare screenshots for critical paths

Example:
```go
// Traditional (breaks when UI changes)
click("#send-button")

// Self-healing (adapts to UI changes)
find({role: "button", label_similarity: "send|submit|go"})
```
