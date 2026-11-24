#!/usr/bin/env bash
# build.sh: build anchor-lab for the lab box, checked first.
#
#   ./build.sh                      with the public sample bench set (bench/set_sample.go)
#   ./build.sh <private set>        with a private set: a .go.txt file that starts //go:build privatebench.
#                                   Its tests, <name>_test.go.txt beside it, run too when they're there.
#                                   ANCHOR_LAB_BENCH_CASES in private/settings.env names it once for all.
#
# The private set is copied into bench/ for the build and removed after, whatever happens, so it
# never ends up in a commit (.gitignore names the copies as well). Writes ./anchor-lab.
set -euo pipefail
cd "$(dirname "${BASH_SOURCE[0]}")"
# our own settings, when they're here (private/README.md)
if [ -f private/settings.env ]; then set -a; . private/settings.env; set +a; fi
cases="${1:-${ANCHOR_LAB_BENCH_CASES:-}}"
tags=()
cleanup() { rm -f bench/zz_private.go bench/zz_private_test.go; }
trap cleanup EXIT
if [ -n "$cases" ]; then
    [ -f "$cases" ] || { echo "no file $cases"; exit 1; }
    head -n1 "$cases" | grep -qx '//go:build privatebench' || { echo "$cases doesn't start with //go:build privatebench, so it isn't a bench set"; exit 1; }
    cp "$cases" bench/zz_private.go
    t="${cases%.go.txt}_test.go.txt"
    [ -f "$t" ] && cp "$t" bench/zz_private_test.go
    tags=(-tags privatebench)
fi
go vet "${tags[@]}" ./...
go test -short "${tags[@]}" ./...
# the version is when it was built and from which commit, with .dirty when the tree had changes
# git hadn't committed: a score names the harness that made it, and a date alone doesn't say what
# code that was (gBench writes the harness's commit and a dirty flag on every result)
commit="$(git rev-parse --short=12 HEAD 2>/dev/null || echo nogit)"
git diff --quiet HEAD -- 2>/dev/null || commit="$commit.dirty"
go build "${tags[@]}" -trimpath -ldflags "-X main.version=$(date -u +%Y.%m.%d-%H%M)+$commit" -o anchor-lab ./cmd/anchor-lab
./anchor-lab version
