# Sourced by the smoke scripts. Sets PY to a Python 3 that actually runs.
#
# On Windows, python3 can be the Microsoft Store placeholder, which only prints
# an install hint, and Python is often installed as python or py instead. So
# each candidate is tried, starting with $PYTHON when it's set.
PY=""
for candidate in "${PYTHON:-}" python3 python py; do
  [ -n "$candidate" ] || continue
  if "$candidate" -c 'import sys; sys.exit(sys.version_info < (3, 8))' >/dev/null 2>&1; then
    PY=$candidate
    break
  fi
done
if [ -z "$PY" ]; then
  echo "FAIL: no Python 3.8 or newer found. Install Python, or set PYTHON to one." >&2
  exit 1
fi
