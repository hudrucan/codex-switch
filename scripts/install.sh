#!/usr/bin/env sh
set -eu
# Run from a reviewed fork checkout; never download and execute an unverified helper.
command -v python3 >/dev/null 2>&1 || { echo "Python 3.9+ is required for verified installation" >&2; exit 1; }
exec python3 "$(dirname "$0")/install.py" "$@"
