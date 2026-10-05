#!/bin/sh
set -eu
if [ "$#" -ne 1 ] || ! printf '%s\n' "$1" | grep -Eq '^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z]+([.-][0-9A-Za-z]+)*)?$'; then
    echo "expected a vX.Y.Z release tag, optionally with a prerelease suffix" >&2
    exit 1
fi
