#!/usr/bin/env bash
# SPDX-License-Identifier: MIT
# Repeats the chaos regression tests under CPU contention, with the race detector.
#
# A race that needs a goroutine to stall at the wrong moment rarely shows on an
# idle machine. These counts found the race in Client.Close on v1.10.3 in two of
# three runs, so a pass does not prove that no race is left.
#
#   stress-load.sh <output dir> [count] [processes]
set -euo pipefail

out="${1:?usage: stress-load.sh <output dir> [count] [processes]}"
count="${2:-100}"
procs="${3:-8}"
cores="$(getconf _NPROCESSORS_ONLN)"

mkdir -p "${out}"
bin="${out}/chaos.test"
go test -c -race -o "${bin}" .

burners=()
stop() { [[ ${#burners[@]} -eq 0 ]] || kill "${burners[@]}" 2>/dev/null || true; }
trap stop EXIT
for ((i = 0; i < cores * 2; i++)); do
	(while :; do :; done) &
	burners+=("$!")
done

pids=()
for ((i = 1; i <= procs; i++)); do
	"${bin}" -test.run '^TestChaosRegression' -test.count="${count}" -test.cpu=1,2 -test.timeout=50m >"${out}/run-${i}.log" 2>&1 &
	pids+=("$!")
done

failed=0
for ((i = 0; i < procs; i++)); do
	if ! wait "${pids[${i}]}"; then
		failed=1
		echo "--- process $((i + 1)) failed"
		grep -E -- '--- FAIL|_test\.go:[0-9]+:|^panic|DATA RACE' "${out}/run-$((i + 1)).log" || tail -n 20 "${out}/run-$((i + 1)).log"
	fi
done
[[ ${failed} -eq 0 ]] && echo "${procs} processes of ${count} repeats passed, under load on ${cores} cores"
exit "${failed}"
