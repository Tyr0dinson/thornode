#!/bin/bash
# Collection of helper utility functions.

# Print a passed in dotted version string that can be compared with
# shell numerical comparators: [ ... -lt ... ]
version() { echo "$@" | awk -F. '{ printf("%d%03d%03d%03d\n", $1,$2,$3,$4); }'; }

# Print error message and exit with a non-zero error code.
die() {
  echo "ERR: $*"
  exit 1
}
