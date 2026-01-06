#!/bin/bash
set -e

# Read JSON from stdin
INPUT=$(cat)

# Parse params with defaults
PACKAGE=$(echo "$INPUT" | jq -r '.package // "./..."')
TAGS=$(echo "$INPUT" | jq -r '.tags // ""')

# Build command
CMD="go vet"

if [ -n "$TAGS" ]; then
    CMD="$CMD -tags=$TAGS"
fi

CMD="$CMD $PACKAGE"

# Run vet and capture output
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
