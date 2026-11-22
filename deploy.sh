#!/usr/bin/env bash
# deploy.sh: bring the lab box up to date in one go, and the lab carries on where it was.
#
#   ./deploy.sh            pull, build, install, and the lab goes on
#   ./deploy.sh -llama     the same, with llama.cpp pulled and rebuilt in its checkout first
#                          (LLAMA_CPP in private/settings.env)
#   ./deploy.sh -no-pull   build and install what's checked out, without pulling
#
# Run it as yourself from the box's clone of this repository, the first time too. It builds in the
# clone as you and asks sudo only for the two installs, install.sh and models/install.sh. sudo asks
# for your password once, at the start, and nothing after that waits for an answer.
#
# In order:
#   1. git pull, fast-forward only, on main
#   2. ./build.sh: vet, the short tests and the binary, with the private bench set when
#      private/settings.env names one
#   3. with -llama: llama.cpp pulled and built in its checkout
#   4. the model the lab uses: any of its files that's missing downloaded and checked
#      (models/fetch.sh current)
#   5. the lab paused, so what's in progress stops at a point it goes on from
#   6. sudo bash models/install.sh: the presets and the unit, and llama-server restarted only if
#      they changed
#   7. sudo bash install.sh: the binary and the units, and the harness restarted
#   8. the list of servers to test, fresh from Anchor Terminal's public API
#   9. the pause lifted: an evaluation goes on from its checkpoint, a retest with the servers it
#      hadn't reached, and a bench from its start
# A pull, a build or an install that fails stops it there. Up to the installs the lab hasn't been
# touched, and once it's paused the pause is lifted however the deploy ends. A model that doesn't
# download or a list that doesn't come is said and passed over, since the rest doesn't need them.
set -euo pipefail

state="${ANCHOR_LAB_STATE:-/var/lib/anchor-lab}" # the lab's state, as fetch.sh reads it

say() { printf '\n== %s\n' "$*"; }
note() { printf '   %s\n' "$*"; }
die() { printf '\ndeploy: %s\n' "$*" >&2; exit 1; }

# ingroup says whether you're in the anchorlab group, which reads the lab's state and writes its
# inbox, and asgroup runs a command with it: as you when this login has it already, through sg when
# install.sh has only just added you (a login picks it up), and not at all otherwise.
ingroup() { getent group anchorlab | cut -d: -f4 | tr ',' '\n' | grep -qx "$(id -un)"; }
asgroup() {
    if id -nG | tr ' ' '\n' | grep -qx anchorlab; then
        "$@"
    elif ingroup; then
        sg anchorlab -c "$(printf '%q ' "$@")"
    else
        return 2
    fi
}

main() {
    local pull=1 llama=0
    for a in "$@"; do
        case "$a" in
            -no-pull) pull=0 ;;
            -llama) llama=1 ;;
            -h | -help | --help) sed -n '2,8p' "$0" | sed 's/^# \{0,1\}//'; exit 0 ;;
            *) die "no flag $a (./deploy.sh -help)" ;;
        esac
    done
    cd "$(dirname "${BASH_SOURCE[0]}")"
    [ "$(id -u)" != 0 ] || die "run it as yourself, not as root: it builds in your clone, and asks sudo for the installs itself"
    [ -d .git ] && [ -f install.sh ] && [ -f build.sh ] || die "run it from the clone of github.com/Anchor-Terminal/anchorlab"
    local missing=""
    for c in git go curl sha256sum; do command -v "$c" >/dev/null || missing="$missing $c"; done
    [ "$llama" = 0 ] || command -v cmake >/dev/null || missing="$missing cmake"
    [ -z "$missing" ] || die "missing:$missing. From Debian: sudo apt install git curl coreutils cmake, and sudo apt install -t trixie-backports golang-go"
    if [ -f private/settings.env ]; then set -a; . private/settings.env; set +a; fi

    # sudo once now, kept fresh while the build and the downloads run, so nothing waits on it later
    say "sudo, for the installs later"
    sudo -v || die "sudo didn't take the password"
    ( while kill -0 $$ 2>/dev/null; do sudo -n -v 2>/dev/null || true; sleep 50; done ) &
    keeper=$!
    paused=0
    trap 'r=$?; [ "$paused" = 1 ] && asgroup ./anchor-lab pause -off -state "$state" >/dev/null 2>&1 && echo "   the pause is lifted"; kill "$keeper" 2>/dev/null; exit $r' EXIT

    # 1. the pull. old is where the clone was before it, carried over when a new deploy.sh takes over
    local old; old="${ANCHOR_LAB_DEPLOY_FROM:-$(git rev-parse HEAD)}"
    if [ "$pull" = 1 ]; then
        say "pull"
        [ "$(git symbolic-ref --short -q HEAD || true)" = main ] || die "the clone isn't on main, so nothing was pulled (git checkout main, or ./deploy.sh -no-pull)"
        [ -z "$(git status --porcelain --untracked-files=no)" ] || die "the clone has changes of its own, so nothing was pulled: $(git status --short --untracked-files=no | tr '\n' ' ')"
        git pull --ff-only --quiet origin main
        if [ "$(git rev-parse HEAD)" = "$old" ]; then
            note "already up to date, at $(git log -1 --format='%h %s')"
        else
            git log --format='   %h %s' "$old..HEAD"
            # the deploy that was just pulled is the one that runs
            if ! git diff --quiet "$old" HEAD -- deploy.sh; then
                note "deploy.sh changed in the pull, so the new one carries on"
                kill "$keeper" 2>/dev/null; trap - EXIT
                ANCHOR_LAB_DEPLOY_FROM="$old" exec ./deploy.sh -no-pull "$@"
            fi
        fi
    fi

    # 2. the build: vet, the short tests and the binary. Nothing is installed if this fails
    say "build"
    ./build.sh

    # 3. llama.cpp, built as you in its checkout; models/install.sh -llama installs it below
    if [ "$llama" = 1 ]; then
        say "llama.cpp"
        [ -n "${LLAMA_CPP:-}" ] && [ -d "$LLAMA_CPP/.git" ] || die "-llama: LLAMA_CPP in private/settings.env names no llama.cpp checkout"
        git -C "$LLAMA_CPP" pull --ff-only --quiet
        note "at $(git -C "$LLAMA_CPP" log -1 --format='%h %s')"
        [ -d "$LLAMA_CPP/build" ] || cmake -S "$LLAMA_CPP" -B "$LLAMA_CPP/build" -DGGML_VULKAN=ON -DCMAKE_BUILD_TYPE=Release >/dev/null
        cmake --build "$LLAMA_CPP/build" -j"$(nproc)" >/dev/null
        note "built"
    fi

    # 4. the model the lab uses, when one of its files is missing or changed in models.txt
    say "the model in use"
    if asgroup models/fetch.sh current || { [ $? = 2 ] && models/fetch.sh current; }; then
        :
    else
        note "it didn't download and check (above), and the rest goes on: the lab can't use it until it does"
    fi

    # 5. the pause: what's in progress stops where it can go on from, before anything restarts
    local installed=0 already=""
    [ -x /usr/local/bin/anchor-lab ] && installed=1
    if [ "$installed" = 1 ]; then
        say "pause the lab"
        if ! ingroup; then
            note "you aren't in the anchorlab group, so it isn't paused: the restart below stops it cleanly"
        else
            already="$(asgroup ./anchor-lab status -json -state "$state" 2>/dev/null | grep -o '"pausedUntil": *"[^"]*"' | cut -d'"' -f4 || true)"
            case "$already" in 0001-*) already="" ;; esac # the zero time: no pause
            if [ -n "$already" ]; then
                note "it was paused already, until $already, and stays paused after the deploy"
            elif asgroup ./anchor-lab pause -for 30m -state "$state" >/dev/null; then
                paused=1
                if systemctl is-active --quiet anchor-lab.service; then
                    # the harness writes harness.paused once it's holding, within seconds of the pause
                    local since seen=0
                    since="$(date -u +%FT%T)"
                    for _ in $(seq 1 30); do
                        if asgroup awk -v s="$since" 'index($0, "\"ev\":\"harness.paused\"") && match($0, /"t":"[^"]*"/) && substr($0, RSTART + 5, 19) >= s { found = 1 } END { exit !found }' "$state/events/$(date -u +%F).ndjson" 2>/dev/null; then
                            seen=1; break
                        fi
                        sleep 2
                    done
                    if [ "$seen" = 1 ]; then
                        note "paused: what was in progress stopped where it goes on from"
                    else
                        note "it didn't say it was holding within a minute; the restart below stops it cleanly anyway"
                    fi
                fi
            fi
        fi
    fi

    # 6. llama-server's presets and unit, and llama.cpp with -llama
    say "the model server"
    local mi=()
    [ "$llama" = 0 ] || mi=(-llama)
    if [ "$llama" = 1 ] || command -v llama-server >/dev/null || [ -x /usr/local/bin/llama-server ]; then
        sudo bash models/install.sh "${mi[@]}" | sed 's/^/   /' || die "models/install.sh failed (above), so the lab wasn't installed"
    else
        note "no llama-server on this machine yet, so its presets wait (MODELS.md, \"llama.cpp\")"
    fi

    # 7. the harness: the binary and the units, and a restart
    say "the lab"
    sudo bash install.sh | sed 's/^/   /' || die "install.sh failed (above)"

    # 8. the list of servers to test, from Anchor Terminal's public API
    say "the list of servers to test"
    if ! ingroup; then
        note "you aren't in the anchorlab group, which install.sh should have put you in, so there's no new list"
    elif ! asgroup ./anchor-lab targets -inbox "$state" 2>&1 | sed 's/^/   /'; then
        note "it didn't come, and the list the lab had stays"
    fi

    # 9. the pause comes off in the trap, however this ends
    say "done"
    note "$(./anchor-lab version)$( [ "$(git rev-parse HEAD)" = "$old" ] || echo ", $(git rev-list --count "$old..HEAD") commits newer than before")"
    if [ "$paused" = 1 ]; then
        note "the lab goes on within 30 seconds: an evaluation from its checkpoint, a retest with what it hadn't reached, a bench from its start"
    elif [ -n "$already" ]; then
        note "the lab is still paused until $already, as it was before the deploy (anchor-lab pause -off to go on now)"
    fi
    if [ -r "$state/inbox" ]; then
        note "how it goes: anchor-lab status -watch"
    else
        note "log in again, and anchor-lab status shows how it goes, with no sudo"
    fi
}

main "$@"
