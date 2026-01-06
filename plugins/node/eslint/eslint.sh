#!/bin/bash
set -e

# Read JSON from stdin
INPUT=$(cat)

# Parse params with defaults
PATH_ARG=$(echo "$INPUT" | jq -r '.path // "."')
FIX=$(echo "$INPUT" | jq -r '.fix // false')
FORMAT=$(echo "$INPUT" | jq -r '.format // "stylish"')
EXTS=$(echo "$INPUT" | jq -r '.ext // [] | join(",")')

# Build command
CMD="eslint"

if [ "$FIX" = "true" ]; then
    CMD="$CMD --fix"
fi

CMD="$CMD --format=$FORMAT"

if [ -n "$EXTS" ]; then
    CMD="$CMD --ext $EXTS"
fi

CMD="$CMD $PATH_ARG"

# Run eslint and capture output
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
