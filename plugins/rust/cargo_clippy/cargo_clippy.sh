#!/bin/bash
set -e

# Read JSON from stdin
INPUT=$(cat)

# Parse params with defaults
PACKAGE=$(echo "$INPUT" | jq -r '.package // ""')
ALL=$(echo "$INPUT" | jq -r '.all // false')
FIX=$(echo "$INPUT" | jq -r '.fix // false')
DENY=$(echo "$INPUT" | jq -r '.deny // [] | map("-D " + .) | join(" ")')

# Build command
CMD="cargo clippy"

if [ "$ALL" = "true" ]; then
    CMD="$CMD --all"
elif [ -n "$PACKAGE" ]; then
    CMD="$CMD --package=$PACKAGE"
fi

if [ "$FIX" = "true" ]; then
    CMD="$CMD --fix"
fi

if [ -n "$DENY" ]; then
    CMD="$CMD -- $DENY"
fi

# Run clippy and capture output
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
