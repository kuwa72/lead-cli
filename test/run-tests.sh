#!/usr/bin/env bash
set -euo pipefail

echo "=== Running tests ==="
success=0
total=0

# Same mandatory gates as CI (issue #280): a change that does not vet or
# build must fail locally before the shell tests.
if [ -f go.mod ]; then
  echo "=== go vet ./... ==="
  go vet ./...
  echo "=== go build ./... ==="
  go build ./...
fi

for test_file in test/test_*.sh; do
  [ -f "$test_file" ] || continue
  total=$((total + 1))
  echo -n "Testing ${test_file}... "
  if bash "$test_file"; then
    echo "PASS"
    success=$((success + 1))
  else
    echo "FAIL"
  fi
done

echo "=== Result: ${success}/${total} passed ==="
[ "$success" -eq "$total" ]

if [ -f go.mod ]; then
  echo "=== go test ./... ==="
  go test ./...
fi
