#!/bin/sh
set -eu

expect_status() {
    expected=$1
    shift
    status=0
    "$@" || status=$?
    if [ "$status" -ne "$expected" ]; then
        echo "expected exit $expected, got $status: $*" >&2
        exit 1
    fi
}

/out/tini --version | grep -Fx "tini version 0.19.0"
expect_status 42 /out/tini -s -- /tests/fixture exit
/out/tini -s -- /tests/fixture orphan

ready=$(mktemp)
supervisor=
cleanup() {
    if [ -n "$supervisor" ]; then
        kill -KILL "$supervisor" 2>/dev/null || :
        wait "$supervisor" 2>/dev/null || :
    fi
    rm -f "$ready"
}
trap cleanup EXIT
trap 'exit 1' INT TERM

/out/tini -s -- /tests/fixture signal > "$ready" &
supervisor=$!
attempt=0
until grep -Fxq ready "$ready"; do
    attempt=$((attempt + 1))
    if [ "$attempt" -ge 50 ] || ! kill -0 "$supervisor" 2>/dev/null; then
        echo "signal fixture did not become ready" >&2
        exit 1
    fi
    sleep 0.1
done
kill -TERM "$supervisor"
attempt=0
while kill -0 "$supervisor" 2>/dev/null; do
    attempt=$((attempt + 1))
    if [ "$attempt" -ge 50 ]; then
        echo "Tini did not exit within five seconds of SIGTERM" >&2
        exit 1
    fi
    sleep 0.1
done
expect_status 42 wait "$supervisor"
supervisor=
echo "Tini exit-code, orphan-reaping, and signal-forwarding tests passed"
