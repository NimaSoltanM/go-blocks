#!/usr/bin/env bash
set -euo pipefail

race_args=()
if [[ $# -gt 1 ]] || [[ $# -eq 1 && $1 != --race ]]; then
  echo 'Usage: bash scripts/check.sh [--race]' >&2
  exit 2
fi
if [[ $# -eq 1 ]]; then
  race_args=(-race)
fi

cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.."
export GOWORK=off

go_files=()
while IFS= read -r -d '' file; do
  go_files+=("$file")
done < <(git ls-files -z --cached --others --exclude-standard -- '*.go')
if [[ ${#go_files[@]} -eq 0 ]]; then
  echo 'No Go source files found.' >&2
  exit 1
fi

unformatted=$(gofmt -l "${go_files[@]}")
if [[ -n $unformatted ]]; then
  printf 'Run gofmt -w on these files:\n%s\n' "$unformatted" >&2
  exit 1
fi

module_files=()
while IFS= read -r -d '' file; do
  module_files+=("$file")
done < <(git ls-files -z --cached --others --exclude-standard -- 'go.mod' '*/go.mod')
if [[ ${#module_files[@]} -eq 0 ]]; then
  echo 'No Go modules found.' >&2
  exit 1
fi
for module_file in "${module_files[@]}"; do
  (
    cd -- "$(dirname -- "$module_file")"
    printf 'Checking %s\n' "$module_file"
    go mod tidy -diff
    go mod verify
    go vet -tags=integration ./...
    go test -count=1 ${race_args[@]+"${race_args[@]}"} ./...
  )
done
go test -tags=integration -run='^$' ./tests/smoke
echo 'Repository checks passed.'
