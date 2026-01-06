#!/bin/bash
set -e

# Read JSON from stdin
INPUT=$(cat)

# Parse params
FILE=$(echo "$INPUT" | jq -r '.file')
ARGS=$(echo "$INPUT" | jq -r '.args // [] | join(" ")')
TRANSPILE_ONLY=$(echo "$INPUT" | jq -r '.transpile_only // false')

# Build command
CMD="ts-node"

if [ "$TRANSPILE_ONLY" = "true" ]; then
    CMD="$CMD --transpile-only"
fi

CMD="$CMD $FILE"

if [ -n "$ARGS" ]; then
    CMD="$CMD $ARGS"
fi

# Run ts-node and capture output
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
