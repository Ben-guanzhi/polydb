#!/bin/sh
# Run one check, stream its full output, and on failure publish the trailing
# non-empty lines as GitHub annotations.
#
# Annotations are the only part of a failed job readable without the raw logs
# (which require a GitHub login), and each annotation message is truncated at
# about 512 bytes, so one annotation per offending line keeps the real error
# inside the budget. A single concatenated message let dependency-download and
# progress noise eat the whole budget and hide the error entirely.
set +e
dir="${RUNNER_TEMP:-${TMPDIR:-/tmp}}"
mkdir -p "$dir"
log="$dir/gate.log"
"$@" >"$log" 2>&1
rc=$?
cat "$log"
if [ "$rc" -ne 0 ]; then
  n=0
  grep -v '^[[:space:]]*$' "$log" | tail -n 80 | while IFS= read -r line; do
    n=$((n + 1))
    if [ "$n" -gt 15 ]; then break; fi
    echo "::error title=$1 rc=$rc [$n]::$(printf '%s' "$line" | tr '\t' ' ' | head -c 450)"
  done
fi
exit "$rc"
