#!/bin/bash
set -e

# Read JSON from stdin
INPUT=$(cat)

# Parse params
RELEASE=$(echo "$INPUT" | jq -r '.release // false')
PACKAGE=$(echo "$INPUT" | jq -r '.package // ""')
ARGS=$(echo "$INPUT" | jq -r '.args // ""')

# Build command
CMD="cargo run"

if [ "$RELEASE" = "true" ]; then
    CMD="$CMD --release"
fi

if [ -n "$PACKAGE" ]; then
    CMD="$CMD -p $PACKAGE"
fi

if [ -n "$ARGS" ]; then
    CMD="$CMD -- $ARGS"
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
