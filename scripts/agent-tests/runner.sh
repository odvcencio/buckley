#!/bin/bash
# Self-healing E2E test runner for Buckley
# Usage: ./runner.sh [--socket PATH] [--scenario FILE] [--verbose] [--list]

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DRIVER_BIN="${SCRIPT_DIR}/.bin/agent-test-driver"

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m' # No Color

# Defaults
SOCKET="unix:/tmp/buckley.sock"
SCENARIO=""
VERBOSE=""
LIST=""

# Parse arguments
while [[ $# -gt 0 ]]; do
    case $1 in
        --socket)
            SOCKET="$2"
            shift 2
            ;;
        --scenario)
            SCENARIO="$2"
            shift 2
            ;;
        --verbose)
            VERBOSE="--verbose"
            shift
            ;;
        --list)
            LIST="1"
            shift
            ;;
        -h|--help)
            echo "Buckley Agent E2E Test Runner"
            echo ""
            echo "Usage: $0 [OPTIONS]"
            echo ""
            echo "Options:"
            echo "  --socket PATH      Agent socket address (default: unix:/tmp/buckley.sock)"
            echo "  --scenario FILE    Run specific scenario file"
            echo "  --verbose          Enable verbose output"
            echo "  --list             List available scenarios"
            echo "  -h, --help         Show this help"
            echo ""
            echo "Examples:"
            echo "  # List available scenarios"
            echo "  $0 --list"
            echo ""
            echo "  # Run smoke test"
            echo "  $0 --scenario scenarios/smoke.json --verbose"
            echo ""
            echo "  # Run all scenarios in directory"
            echo "  $0 --scenario scenarios/"
            exit 0
            ;;
        *)
            echo "Unknown option: $1"
            exit 1
            ;;
    esac
done

# List scenarios
if [[ -n "$LIST" ]]; then
    echo -e "${BLUE}Available scenarios:${NC}"
    for f in "${SCRIPT_DIR}"/scenarios/*.json; do
        if [[ -f "$f" ]]; then
            name=$(basename "$f" .json)
            desc=$(jq -r '.description // "No description"' "$f" 2>/dev/null || echo "No description")
            echo "  ${GREEN}$(basename "$f")${NC}"
            echo "    $desc"
        fi
    done
    exit 0
fi

# Build driver if needed
if [[ ! -f "$DRIVER_BIN" ]] || [[ "${SCRIPT_DIR}"/*.go -nt "$DRIVER_BIN" ]]; then
    echo -e "${BLUE}Building agent test driver...${NC}"
    mkdir -p "$(dirname "$DRIVER_BIN")"
    go build -o "$DRIVER_BIN" "${SCRIPT_DIR}"/*.go
    echo -e "${GREEN}✓ Driver built${NC}"
fi

# Run tests
if [[ -n "$SCENARIO" ]]; then
    # Run specific scenario
    if [[ -f "$SCENARIO" ]]; then
        echo -e "${BLUE}Running scenario:${NC} $(basename "$SCENARIO")"
        "$DRIVER_BIN" --socket "$SOCKET" --scenario "$SCENARIO" $VERBOSE
    elif [[ -d "$SCENARIO" ]]; then
        # Run all scenarios in directory
        echo -e "${BLUE}Running all scenarios in:${NC} $SCENARIO"
        failed=0
        passed=0
        
        for f in "$SCENARIO"/*.json; do
            if [[ -f "$f" ]]; then
                echo ""
                echo -e "${BLUE}═══════════════════════════════════════════════════${NC}"
                echo -e "${BLUE}Scenario:${NC} $(basename "$f")"
                echo -e "${BLUE}═══════════════════════════════════════════════════${NC}"
                
                if "$DRIVER_BIN" --socket "$SOCKET" --scenario "$f" $VERBOSE; then
                    echo -e "${GREEN}✓ PASSED${NC}"
                    ((passed++))
                else
                    echo -e "${RED}✗ FAILED${NC}"
                    ((failed++))
                fi
            fi
        done
        
        echo ""
        echo -e "${BLUE}═══════════════════════════════════════════════════${NC}"
        echo -e "${GREEN}Passed: $passed${NC}  ${RED}Failed: $failed${NC}"
        echo -e "${BLUE}═══════════════════════════════════════════════════${NC}"
        
        if [[ $failed -gt 0 ]]; then
            exit 1
        fi
    else
        echo -e "${RED}Error:${NC} Scenario not found: $SCENARIO"
        exit 1
    fi
else
    # Run interactive demo mode
    echo -e "${BLUE}Running demo mode...${NC}"
    echo "Use --scenario to run a specific test"
    echo ""
    "$DRIVER_BIN" --socket "$SOCKET" $VERBOSE
fi
