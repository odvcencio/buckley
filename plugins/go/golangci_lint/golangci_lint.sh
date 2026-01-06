#!/bin/bash
set -e

# Read JSON from stdin
INPUT=$(cat)

# Parse params with defaults
PATH_ARG=$(echo "$INPUT" | jq -r '.path // "./..."')
ENABLE=$(echo "$INPUT" | jq -r '.enable // [] | join(",")')
DISABLE=$(echo "$INPUT" | jq -r '.disable // [] | join(",")')
FIX=$(echo "$INPUT" | jq -r '.fix // false')

# Build command
CMD="golangci-lint run"

if [ -n "$ENABLE" ]; then
    CMD="$CMD --enable=$ENABLE"
fi

if [ -n "$DISABLE" ]; then
    CMD="$CMD --disable=$DISABLE"
fi

if [ "$FIX" = "true" ]; then
    CMD="$CMD --fix"
fi

CMD="$CMD $PATH_ARG"

# Run linter and capture output
OUTPUT=$(eval "$CMD" 2>&1)
EXIT_CODE=$?

# Determine success (exit code 0 = no issues)
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
