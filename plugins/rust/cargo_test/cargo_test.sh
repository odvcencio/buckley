#!/bin/bash
set -e

# Read JSON from stdin
INPUT=$(cat)

# Parse params
PACKAGE=$(echo "$INPUT" | jq -r '.package // ""')
VERBOSE=$(echo "$INPUT" | jq -r '.verbose // false')
RELEASE=$(echo "$INPUT" | jq -r '.release // false')

# Build command
CMD="cargo test"

if [ -n "$PACKAGE" ]; then
    CMD="$CMD -p $PACKAGE"
fi

if [ "$VERBOSE" = "true" ]; then
    CMD="$CMD --verbose"
fi

if [ "$RELEASE" = "true" ]; then
    CMD="$CMD --release"
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
