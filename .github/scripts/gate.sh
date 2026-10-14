#!/bin/sh
# Run one check, stream its full output, and on failure publish the lines that
# explain the failure as GitHub annotations.
#
# Annotations are the only part of a failed job readable without the raw logs
# (which require a GitHub login), and each annotation message is truncated at
# about 512 bytes, so one annotation per offending line keeps the real error
# inside the budget.
#
# Picking the right lines is the hard part. rustc and cargo print the error in
# the middle of the log and then hundreds of "Checking foo v1.2.3" progress
# lines after it, so a plain tail returns only progress and hides the error
# entirely. Worse, Rust's stdout is block-buffered when redirected to a file,
# so the "test ... ok" lines are flushed at process exit, i.e. after the
# unbuffered stderr failure summary. Prefer lines that describe a failure, and
# only fall back to the tail when nothing recognisable matches.
set +e

dir="${RUNNER_TEMP:-${TMPDIR:-/tmp}}"
mkdir -p "$dir"
log="$dir/gate.log"

"$@" >"$log" 2>&1
rc=$?
cat "$log"

if [ "$rc" -ne 0 ]; then
  # Rust and Go honour CARGO_TERM_COLOR / GO_COLORS, so colour escapes surround
  # the keywords: an error line arrives as "\033[0merror\033[0m:". Strip them
  # BEFORE matching, or a grep for "error:" silently misses it.
  clean="$dir/gate.clean"
  sed 's/\x1B\[[0-9;]*[A-Za-z]//g; s/\x1B[()][0-9A-Za-z]//g' "$log" >"$clean"

  # 1st pass: hard errors and their source locations.
  grep -nE 'error(\[|:)|-->|--- FAIL|FAILED|panicked at|^failures:|undefined:|no such file|not found|exit status [0-9]|command not found|\.go:[0-9]+:' "$clean" >"$dir/gate.err"
  # 2nd pass: warnings, which clippy promotes to errors via -D warnings.
  if [ ! -s "$dir/gate.err" ]; then
    grep -nE 'warning(\[|:)|-->|cannot find|unresolved import|expected [0-9]+ arguments|too many arguments' "$clean" >"$dir/gate.err"
  fi
  # 3rd pass: nothing recognisable, so just give the tail.
  if [ ! -s "$dir/gate.err" ]; then
    grep -v '^[[:space:]]*$' "$clean" | tail -n 80 >"$dir/gate.err"
  fi

  i=0
  head -n 15 "$dir/gate.err" | while IFS= read -r line; do
    i=$((i + 1))
    # A literal % must be escaped as %25 or the runner silently drops the
    # whole annotation.
    cleanline=$(printf '%s' "$line" | sed 's/%/%25/g' | tr '\t' ' ' | head -c 450)
    echo "::error title=$1 rc=$rc [$i]::${cleanline}"
  done
fi

exit "$rc"
