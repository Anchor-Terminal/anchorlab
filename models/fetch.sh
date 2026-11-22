#!/usr/bin/env bash
# models/fetch.sh: download the models in models.txt and check each file against its SHA-256.
#
#   models/fetch.sh list                  what's in the list, and what's here already
#   models/fetch.sh gpt-oss-120b          one model (every file it has)
#   models/fetch.sh current               the model the lab uses now, if any of its files is missing
#   models/fetch.sh all                   every model, about 1.02 TB
#   models/fetch.sh -check all            hash what's here again, download nothing
#   models/fetch.sh remove qwen3.6-27b    delete a model's files (not the one the lab uses)
#   models/fetch.sh swap OLD NEW          download NEW, switch the lab to it, then delete OLD
#   models/fetch.sh prune                 delete files in $MODELS that no line of models.txt names
#
# Files go in $MODELS/<folder>/ (models.txt has the folder; MODELS defaults to /srv/models). A
# download that stops is picked up where it stopped. A finished file is hashed once and a
# .sha256 beside it records the result, so a second run doesn't read 60 GB again; -check reads it
# all again. Needs curl and sha256sum, nothing else. Run it as a user that can write $MODELS.
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# our own settings, when they're here (private/README.md); MODELS from the environment wins
if [ -f "$here/../private/settings.env" ]; then
    keep="${MODELS:-}"; set -a; . "$here/../private/settings.env"; set +a; [ -z "$keep" ] || MODELS="$keep"
fi
list="$here/models.txt"
MODELS="${MODELS:-/srv/models}"
check=0
if [ "${1:-}" = "-check" ]; then check=1; shift; fi
[ $# -gt 0 ] || { sed -n '2,13p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 2; }

rows() { grep -v '^\s*#' "$list" | grep -v '^\s*$'; }
names() { rows | awk '{print $1}' | uniq; }
human() { awk -v b="$1" 'BEGIN { printf "%.1f GB", b/1e9 }'; }

if [ "$1" = "list" ]; then
    printf '%-18s %-10s %s\n' "name" "size" "here"
    for n in $(names); do
        total=0; have="yes"
        while read -r name folder repo file bytes sha; do
            total=$((total + bytes))
            [ -f "$MODELS/$folder/$file.sha256" ] && [ "$(cat "$MODELS/$folder/$file.sha256")" = "$sha" ] || have="no"
        done < <(rows | awk -v n="$n" '$1 == n')
        printf '%-18s %-10s %s\n' "$n" "$(human $total)" "$have"
    done
    exit 0
fi

# the model the lab uses now: the inbox's choice, else the one it started with
STATE="${ANCHOR_LAB_STATE:-/var/lib/anchor-lab}"
in_use() {
    local m=""
    [ -r "$STATE/inbox/model" ] && m="$(head -n1 "$STATE/inbox/model" | tr -d '[:space:]')"
    if [ -z "$m" ] && [ -r /etc/anchor-lab/env ]; then
        m="$(sed -n 's/^ANCHOR_LAB_MODEL=//p' /etc/anchor-lab/env | tail -n1 | tr -d "\"'[:space:]")"
    fi
    echo "$m"
}

# remove deletes every file of the named models (and their .sha256 and any part-download). It
# refuses the model the lab uses, unless -force: switch first (swap does both).
remove() {
    local force=0; [ "${1:-}" = "-force" ] && { force=1; shift; }
    [ $# -gt 0 ] || { echo "remove: which model (models/fetch.sh list)"; exit 2; }
    local cur; cur="$(in_use)"
    for n in "$@"; do
        rows | awk -v n="$n" '$1 == n' | grep -q . || { echo "no model $n in models.txt (models/fetch.sh list; prune deletes files no line names)"; exit 1; }
        if [ "$n" = "$cur" ] && [ "$force" = 0 ]; then
            echo "$n is the model the lab uses: switch first (models/fetch.sh swap $n <another>, or anchor-lab model use <another>), or -force"; exit 1
        fi
    done
    local freed=0
    local gone=" $* "
    for n in "$@"; do
        while read -r name folder repo file bytes sha; do
            path="$MODELS/$folder/$file"
            # a file another model in the list loads stays (a variant shares its model's file)
            keep="$(rows | awk -v f="$folder/$file" -v gone="$gone" '($2 "/" $4) == f && index(gone, " " $1 " ") == 0 {print $1; exit}')"
            if [ -n "$keep" ]; then
                echo "$name: kept $file, which $keep loads too"
                continue
            fi
            if [ -f "$path" ]; then freed=$((freed + $(stat -c %s "$path"))); fi
            rm -f -- "$path" "$path.sha256"
            echo "$name: deleted $file"
        done < <(rows | awk -v n="$n" '$1 == n')
        # the folder too, and any build folder in it, once nothing is left
        folder="$(rows | awk -v n="$n" '$1 == n {print $2; exit}')"
        [ -d "$MODELS/$folder" ] && find "$MODELS/$folder" -depth -type d -empty -delete 2>/dev/null || true
    done
    echo "freed $(human $freed) in $MODELS ($(human "$(free_bytes "$MODELS")") free now)"
}

if [ "$1" = "remove" ]; then
    shift
    free_bytes() { df -B1 --output=avail "$1" | tail -n1 | tr -d ' '; }
    remove "$@"
    exit 0
fi

if [ "$1" = "prune" ]; then
    free_bytes() { df -B1 --output=avail "$1" | tail -n1 | tr -d ' '; }
    known="$(rows | awk '{print $2 "/" $4}')"
    freed=0; n=0
    while IFS= read -r -d '' path; do
        rel="${path#"$MODELS"/}"; rel="${rel%.sha256}"
        if ! grep -qxF -- "$rel" <<<"$known"; then
            [ -f "$path" ] && freed=$((freed + $(stat -c %s "$path")))
            rm -f -- "$path"; n=$((n + 1)); echo "deleted $rel$([ "${path%.sha256}" != "$path" ] && echo .sha256)"
        fi
    done < <(find "$MODELS" -type f \( -name '*.gguf' -o -name '*.gguf.sha256' \) -print0)
    find "$MODELS" -mindepth 1 -type d -empty -delete 2>/dev/null || true
    echo "deleted $n files no line of models.txt names, freed $(human $freed)"
    exit 0
fi

if [ "$1" = "swap" ]; then
    [ $# -eq 3 ] || { echo "swap: models/fetch.sh swap OLD NEW"; exit 2; }
    old="$2"; new="$3"
    for n in "$old" "$new"; do
        rows | awk -v n="$n" '$1 == n' | grep -q . || { echo "no model $n in models.txt (models/fetch.sh list)"; exit 1; }
    done
    [ "$old" != "$new" ] || { echo "swap: two different models"; exit 2; }
    "${BASH_SOURCE[0]}" "$new" || { echo "swap: $new didn't download and check, so nothing was switched or deleted"; exit 1; }
    if [ "$(in_use)" = "$old" ]; then
        lab="$(command -v anchor-lab || true)"
        [ -n "$lab" ] || { echo "swap: no anchor-lab on PATH to switch the lab; $old is still in use and still here"; exit 1; }
        "$lab" model use "$new" || { echo "swap: the lab didn't switch to $new (is it in /etc/llama/models.ini? sudo bash models/install.sh puts a new model's preset there); $old is still here"; exit 1; }
    fi
    free_bytes() { df -B1 --output=avail "$1" | tail -n1 | tr -d ' '; }
    remove -force "$old"
    exit 0
fi

want=("$@")
[ "$1" = "all" ] && mapfile -t want < <(names)
# the model the lab uses now (deploy.sh asks for it): what's here and checked is passed over in a
# moment, so this downloads only what's missing or changed in models.txt
if [ "$1" = "current" ]; then
    cur="$(in_use)"
    [ -n "$cur" ] || { echo "the lab has no model yet, so there's nothing to fetch"; exit 0; }
    rows | awk -v n="$cur" '$1 == n' | grep -q . || { echo "$cur, the model the lab uses, isn't in models.txt, so there's nothing to fetch for it"; exit 0; }
    want=("$cur")
fi
for n in "${want[@]}"; do
    rows | awk -v n="$n" '$1 == n' | grep -q . || { echo "no model $n in models.txt (models/fetch.sh list)"; exit 1; }
done

free_bytes() { df -B1 --output=avail "$1" | tail -n1 | tr -d ' '; }
mkdir -p "$MODELS"
failed=0
for n in "${want[@]}"; do
    while read -r name folder repo file bytes sha; do
        dir="$MODELS/$folder"; path="$dir/$file"
        dir="$(dirname "$path")"   # some repositories keep a build in a folder of its own
        mkdir -p "$dir"
        if [ "$check" = 0 ] && [ -f "$path.sha256" ] && [ "$(cat "$path.sha256")" = "$sha" ] && [ "$(stat -c %s "$path" 2>/dev/null || echo 0)" = "$bytes" ]; then
            echo "$name: $file is here and checked"
            continue
        fi
        if [ "$check" = 0 ]; then
            have=$(stat -c %s "$path" 2>/dev/null || echo 0)
            need=$((bytes - have))
            if [ "$need" -gt 0 ] && [ "$need" -gt "$(free_bytes "$dir")" ]; then
                echo "$name: $file needs $(human $need) more and $dir has $(human "$(free_bytes "$dir")") free"; failed=1; continue
            fi
            if [ "$have" -lt "$bytes" ]; then
                echo "$name: $file, $(human $bytes)$([ "$have" -gt 0 ] && echo ", from $(human $have)")"
                curl -fL --retry 5 --retry-delay 10 -C - -o "$path" "https://huggingface.co/$repo/resolve/main/$file"
            fi
        fi
        [ -f "$path" ] || { echo "$name: $file isn't here"; failed=1; continue; }
        got=$(stat -c %s "$path")
        if [ "$got" != "$bytes" ]; then
            echo "$name: $file is $got bytes, models.txt says $bytes. Delete it and run again, or update the line if the publisher replaced it"; failed=1; continue
        fi
        echo "$name: checking $file"
        sum=$(sha256sum "$path" | awk '{print $1}')
        if [ "$sum" != "$sha" ]; then
            rm -f "$path.sha256"
            echo "$name: $file's SHA-256 is $sum, models.txt says $sha. Not used: delete it and run again"; failed=1; continue
        fi
        echo "$sha" > "$path.sha256"
        echo "$name: $file is checked"
    done < <(rows | awk -v n="$n" '$1 == n')
done
exit $failed
