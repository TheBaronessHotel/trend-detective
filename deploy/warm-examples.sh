#!/bin/sh
# Runs every "Try an example" job once so NASA's data is cached, for
# `make warm-examples`. See warm-examples.py.
set -eu
. "$(dirname "$0")/smoke/python.sh"
"$PY" -I "$(dirname "$0")/warm-examples.py" "$@"
