#!/bin/bash
set -e

# Read JSON from stdin
INPUT=$(cat)

# Parse params
WATCH=$(echo "$INPUT" | jq -r '.watch // false')
COVERAGE=$(echo "$INPUT" | jq -r '.coverage // false')
ARGS=$(echo "$INPUT" | jq -r '.args // ""')

# Build command
CMD="npm test"

# Add flags
EXTRA_ARGS=""
if [ "$WATCH" = "true" ]; then
    EXTRA_ARGS="$EXTRA_ARGS --watch"
fi

if [ "$COVERAGE" = "true" ]; then
    EXTRA_ARGS="$EXTRA_ARGS --coverage"
fi

if [ -n "$ARGS" ]; then
    EXTRA_ARGS="$EXTRA_ARGS $ARGS"
fi

if [ -n "$EXTRA_ARGS" ]; then
    CMD="$CMD -- $EXTRA_ARGS"
fi

# Run and capture output
OUTPUT=$(eval "$CMD" 2>&1)
EXIT_CODE=$?

# Determine success
if [ $EXIT_CODE -eq 0 ]; then
    SUCCESS=true
else
    SUCCESS=false
fi

# Return JSON
cat <<EOF
{
  "success": $SUCCESS,
  "data": {
    "output": $(echo "$OUTPUT" | jq -Rs .),
    "exit_code": $EXIT_CODE
  }
}
EOF
