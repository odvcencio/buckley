#!/bin/bash
set -e

# Read JSON from stdin
INPUT=$(cat)

# Parse params
OUTPUT=$(echo "$INPUT" | jq -r '.output // ""')
PACKAGE=$(echo "$INPUT" | jq -r '.package // "."')

# Build command
CMD="go build"

if [ -n "$OUTPUT" ]; then
    CMD="$CMD -o $OUTPUT"
fi

CMD="$CMD $PACKAGE"

# Run build and capture output
OUTPUT_TEXT=$(eval "$CMD" 2>&1)
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
    "output": $(echo "$OUTPUT_TEXT" | jq -Rs .),
    "exit_code": $EXIT_CODE
  }
}
EOF
