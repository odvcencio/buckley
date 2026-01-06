#!/bin/bash
#
# Buckley Repository Migration Script
# Usage: ./scripts/migrate-repo.sh <new-org> <new-repo>
# Example: ./scripts/migrate-repo.sh myorg buckley
#
# This script updates all references from github.com/odvcencio/buckley to the new location.
# Run this BEFORE pushing to the new repository.

set -euo pipefail

OLD_MODULE="github.com/odvcencio/buckley"
OLD_ORG="odvcencio"
OLD_REPO="buckley"

if [ $# -lt 2 ]; then
    echo "Usage: $0 <new-org> <new-repo>"
    echo "Example: $0 myorg buckley"
    exit 1
fi

NEW_ORG="$1"
NEW_REPO="$2"
NEW_MODULE="github.com/${NEW_ORG}/${NEW_REPO}"

echo "=== Buckley Repository Migration ==="
echo "From: ${OLD_MODULE}"
echo "To:   ${NEW_MODULE}"
echo ""

# Confirm
read -p "Continue? (y/N) " -n 1 -r
echo
if [[ ! $REPLY =~ ^[Yy]$ ]]; then
    echo "Aborted."
    exit 1
fi

echo ""
echo "[1/6] Updating go.mod module path..."
sed -i "s|module ${OLD_MODULE}|module ${NEW_MODULE}|g" go.mod

echo "[2/6] Updating Go import statements (this may take a moment)..."
find . -name "*.go" -type f ! -path "./vendor/*" ! -path "./.git/*" -exec \
    sed -i "s|${OLD_MODULE}|${NEW_MODULE}|g" {} +

echo "[3/6] Updating .goreleaser.yaml..."
sed -i "s|owner: ${OLD_ORG}|owner: ${NEW_ORG}|g" .goreleaser.yaml
sed -i "s|name: ${OLD_REPO}|name: ${NEW_REPO}|g" .goreleaser.yaml
sed -i "s|${OLD_ORG}/${OLD_REPO}|${NEW_ORG}/${NEW_REPO}|g" .goreleaser.yaml

echo "[4/6] Updating documentation files..."
find . -name "*.md" -type f ! -path "./vendor/*" ! -path "./.git/*" ! -path "./node_modules/*" -exec \
    sed -i "s|${OLD_ORG}/${OLD_REPO}|${NEW_ORG}/${NEW_REPO}|g" {} +

echo "[5/6] Updating protobuf files..."
find . -name "*.proto" -type f -exec \
    sed -i "s|${OLD_MODULE}|${NEW_MODULE}|g" {} +

echo "[6/6] Running go mod tidy..."
go mod tidy

echo ""
echo "=== Migration Complete ==="
echo ""
echo "IMPORTANT: Manual steps required:"
echo ""
echo "1. Update SECURITY.md email address:"
echo "   - Current: security@buckley.dev"
echo "   - Change to your security contact"
echo ""
echo "2. Update version for v1.0.0 in cmd/buckley/main.go:"
echo "   - Current: version = \"1.0.0-dev\""
echo "   - Change to: version = \"1.0.0\""
echo ""
echo "3. Update CHANGELOG.md for v1.0.0 release"
echo ""
echo "4. Regenerate protobuf files:"
echo "   make proto"
echo ""
echo "5. Run tests to verify:"
echo "   ./scripts/test.sh"
echo ""
echo "6. Build to verify:"
echo "   go build -o buckley ./cmd/buckley"
echo ""
echo "7. Create new repo and push:"
echo "   git remote set-url origin git@github.com:${NEW_ORG}/${NEW_REPO}.git"
echo "   git push -u origin main"
echo ""
echo "8. Tag v1.0.0:"
echo "   git tag -a v1.0.0 -m \"Initial stable release\""
echo "   git push origin v1.0.0"
