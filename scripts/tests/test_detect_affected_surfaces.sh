#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
tmpdir="$(mktemp -d)"
trap 'rm -rf "$tmpdir"' EXIT

new_fixture() {
  local dir
  dir="$(mktemp -d "$tmpdir/repo.XXXXXX")"
  git init -q "$dir"
  git -C "$dir" config user.email ci-fixture@example.invalid
  git -C "$dir" config user.name ci-fixture
  printf '%s\n' "$dir"
}

commit_fixture() {
  local dir="$1" message="$2"
  git -C "$dir" add -A
  git -C "$dir" commit -qm "$message"
}

run_detection() {
  local dir="$1" base_sha="$2" head_sha="$3" name="$4"
  local output="$tmpdir/$name.output"
  : >"$output"
  (
    cd "$dir"
    BASE_SHA="$base_sha" GITHUB_SHA="$head_sha" GITHUB_OUTPUT="$output" \
      "$repo_root/scripts/detect-affected-surfaces.sh"
  ) >"$tmpdir/$name.stdout" 2>"$tmpdir/$name.stderr"
  tr -d '\r' <"$output"
}

expect_surfaces() {
  local name="$1" dir="$2" base_sha="$3" head_sha="$4" expected="$5"
  local got
  got="$(run_detection "$dir" "$base_sha" "$head_sha" "$name")"
  if [[ "$got" != "$expected" ]]; then
    printf 'surface detection %s = %q, want %q\n' "$name" "$got" "$expected" >&2
    cat "$tmpdir/$name.stderr" >&2
    exit 1
  fi
}

ordinary_dir="$(new_fixture)"
printf '%s\n' 'ordinary' >"$ordinary_dir/README.md"
commit_fixture "$ordinary_dir" initial
ordinary_base="$(git -C "$ordinary_dir" rev-parse HEAD)"
expect_surfaces untouched "$ordinary_dir" "$ordinary_base" "$ordinary_base" $'go=false\nui=false\nrelease=false'
printf '%s\n' 'ordinary change' >"$ordinary_dir/README.md"
commit_fixture "$ordinary_dir" change
ordinary_head="$(git -C "$ordinary_dir" rev-parse HEAD)"
expect_surfaces ordinary-change "$ordinary_dir" "$ordinary_base" "$ordinary_head" $'go=false\nui=false\nrelease=false'

go_delete_dir="$(new_fixture)"
mkdir -p "$go_delete_dir/pkg"
printf '%s\n' 'package pkg' >"$go_delete_dir/pkg/deleted.go"
commit_fixture "$go_delete_dir" initial
go_delete_base="$(git -C "$go_delete_dir" rev-parse HEAD)"
rm "$go_delete_dir/pkg/deleted.go"
commit_fixture "$go_delete_dir" delete-go
go_delete_head="$(git -C "$go_delete_dir" rev-parse HEAD)"
expect_surfaces delete-go "$go_delete_dir" "$go_delete_base" "$go_delete_head" $'go=true\nui=false\nrelease=false'
format_files="$(git -C "$go_delete_dir" diff --no-renames --diff-filter=d --name-only "$go_delete_base" "$go_delete_head" -- '*.go')"
if [[ -n "$format_files" ]]; then
  printf 'deleted Go paths entered the formatting file list: %s\n' "$format_files" >&2
  exit 1
fi

ui_delete_dir="$(new_fixture)"
mkdir -p "$ui_delete_dir/pkg/ipc/gosxui"
printf '%s\n' 'ui' >"$ui_delete_dir/pkg/ipc/gosxui/deleted.txt"
commit_fixture "$ui_delete_dir" initial
ui_delete_base="$(git -C "$ui_delete_dir" rev-parse HEAD)"
rm "$ui_delete_dir/pkg/ipc/gosxui/deleted.txt"
commit_fixture "$ui_delete_dir" delete-ui
ui_delete_head="$(git -C "$ui_delete_dir" rev-parse HEAD)"
expect_surfaces delete-ui "$ui_delete_dir" "$ui_delete_base" "$ui_delete_head" $'go=false\nui=true\nrelease=false'

release_delete_dir="$(new_fixture)"
printf '%s\n' 'goreleaser' >"$release_delete_dir/.goreleaser.yaml"
commit_fixture "$release_delete_dir" initial
release_delete_base="$(git -C "$release_delete_dir" rev-parse HEAD)"
rm "$release_delete_dir/.goreleaser.yaml"
commit_fixture "$release_delete_dir" delete-release
release_delete_head="$(git -C "$release_delete_dir" rev-parse HEAD)"
expect_surfaces delete-release "$release_delete_dir" "$release_delete_base" "$release_delete_head" $'go=false\nui=false\nrelease=true'

rename_ui_dir="$(new_fixture)"
mkdir -p "$rename_ui_dir/pkg/ipc/gosxui"
printf '%s\n' 'package ui' >"$rename_ui_dir/pkg/ipc/gosxui/view.go"
commit_fixture "$rename_ui_dir" initial
rename_ui_base="$(git -C "$rename_ui_dir" rev-parse HEAD)"
mkdir -p "$rename_ui_dir/internal"
git -C "$rename_ui_dir" mv pkg/ipc/gosxui/view.go internal/view.txt
commit_fixture "$rename_ui_dir" rename-out-of-ui
rename_ui_head="$(git -C "$rename_ui_dir" rev-parse HEAD)"
expect_surfaces rename-out-of-ui "$rename_ui_dir" "$rename_ui_base" "$rename_ui_head" $'go=true\nui=true\nrelease=false'

rename_release_dir="$(new_fixture)"
mkdir -p "$rename_release_dir/pkg/version"
printf '%s\n' 'version metadata' >"$rename_release_dir/pkg/version/version.txt"
commit_fixture "$rename_release_dir" initial
rename_release_base="$(git -C "$rename_release_dir" rev-parse HEAD)"
mkdir -p "$rename_release_dir/docs"
git -C "$rename_release_dir" mv pkg/version/version.txt docs/version.txt
commit_fixture "$rename_release_dir" rename-out-of-release
rename_release_head="$(git -C "$rename_release_dir" rev-parse HEAD)"
expect_surfaces rename-out-of-release "$rename_release_dir" "$rename_release_base" "$rename_release_head" $'go=false\nui=false\nrelease=true'

expect_surfaces unknown-base "$ordinary_dir" unknown-base "$ordinary_head" $'go=true\nui=true\nrelease=true'
grep -q 'base commit unavailable' "$tmpdir/unknown-base.stderr"

printf '%s\n' 'affected-surface detection tests passed'
