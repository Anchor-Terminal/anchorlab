#!/usr/bin/env bash
# models/fetch.sh: download the models in models.txt and check each file against its SHA-256.
#
#   models/fetch.sh list                  what's in the list, and what's here already
#   models/fetch.sh gpt-oss-120b          one model (every file it has)
#   models/fetch.sh all                   every model, about 277 GB
#   models/fetch.sh -check all            hash what's here again, download nothing
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
[ $# -gt 0 ] || { sed -n '2,12p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 2; }

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

want=("$@")
[ "$1" = "all" ] && mapfile -t want < <(names)
for n in "${want[@]}"; do
    rows | awk -v n="$n" '$1 == n' | grep -q . || { echo "no model $n in models.txt (models/fetch.sh list)"; exit 1; }
done

free_bytes() { df -B1 --output=avail "$1" | tail -n1 | tr -d ' '; }
mkdir -p "$MODELS"
failed=0
for n in "${want[@]}"; do
    while read -r name folder repo file bytes sha; do
        dir="$MODELS/$folder"; path="$dir/$file"
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
