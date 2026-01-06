#!/bin/bash
set -e

# Read JSON from stdin
INPUT=$(cat)

# Parse params with defaults
PATH_ARG=$(echo "$INPUT" | jq -r '.path // "."')
WRITE=$(echo "$INPUT" | jq -r '.write // false')
CHECK=$(echo "$INPUT" | jq -r '.check // false')
PARSER=$(echo "$INPUT" | jq -r '.parser // ""')

# Build command
CMD="prettier"

if [ "$WRITE" = "true" ]; then
    CMD="$CMD --write"
fi

if [ "$CHECK" = "true" ]; then
    CMD="$CMD --check"
fi

if [ -n "$PARSER" ]; then
    CMD="$CMD --parser=$PARSER"
fi

CMD="$CMD $PATH_ARG"

# Run prettier and capture output
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
