#!/bin/bash
set -e

# Read JSON from stdin
INPUT=$(cat)

# Parse params
SCRIPT=$(echo "$INPUT" | jq -r '.script')
ARGS=$(echo "$INPUT" | jq -r '.args // ""')

# Validate script is provided
if [ "$SCRIPT" = "null" ] || [ -z "$SCRIPT" ]; then
    cat <<EOF
{
  "success": false,
  "error": "script parameter is required"
}
EOF
    exit 0
fi

# Build command
CMD="npm run $SCRIPT"

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
