#!/bin/bash
set -e

# Read JSON from stdin
INPUT=$(cat)

# Parse params with defaults
PACKAGE=$(echo "$INPUT" | jq -r '.package // "./..."')
VERBOSE=$(echo "$INPUT" | jq -r '.verbose // false')
COVERAGE=$(echo "$INPUT" | jq -r '.coverage // false')

# Build command
CMD="go test"

if [ "$VERBOSE" = "true" ]; then
    CMD="$CMD -v"
fi

if [ "$COVERAGE" = "true" ]; then
    CMD="$CMD -cover"
fi

CMD="$CMD $PACKAGE"

# Run tests and capture output
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
