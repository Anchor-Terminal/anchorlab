#!/usr/bin/env bash
# models/install.sh: set up llama-server for the lab's models. Run as root, after llama.cpp is built
# and installed (MODELS.md, "llama.cpp"). Safe to run again: it replaces the presets and the unit.
#
#   MODELS=/srv/models bash models/install.sh
#   bash models/install.sh -llama      and first install llama.cpp from its build in $LLAMA_CPP/build
#
# Makes the user `llama` (no shell, no home, in the render and video groups for the GPU), puts
# presets.ini in /etc/llama/models.ini and the unit in /etc/systemd/system/, and starts it. It
# restarts a running llama-server only when one of those, or llama.cpp itself, changed: a restart
# unloads the model, and deploy.sh runs this every time.
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# our own settings, when they're here (private/README.md); MODELS from the environment wins
if [ -f "$here/../private/settings.env" ]; then
    keep="${MODELS:-}"; set -a; . "$here/../private/settings.env"; set +a; [ -z "$keep" ] || MODELS="$keep"
fi
MODELS="${MODELS:-/srv/models}"
[ "$(id -u)" = 0 ] || { echo "run as root"; exit 1; }
changed=""
# llama.cpp, from the build deploy.sh -llama made in its checkout as an ordinary user (MODELS.md)
if [ "${1:-}" = "-llama" ]; then
    [ -n "${LLAMA_CPP:-}" ] && [ -d "$LLAMA_CPP/build" ] || { echo "-llama: LLAMA_CPP in private/settings.env names no llama.cpp checkout with a build in it"; exit 1; }
    before="$(/usr/local/bin/llama-server --version 2>&1 | head -n1 || true)"
    cmake --install "$LLAMA_CPP/build" --prefix /usr/local >/dev/null
    ldconfig
    after="$(/usr/local/bin/llama-server --version 2>&1 | head -n1 || true)"
    [ "$before" = "$after" ] || changed="llama.cpp"
fi
command -v llama-server >/dev/null || [ -x /usr/local/bin/llama-server ] || { echo "no llama-server: build and install llama.cpp first (MODELS.md)"; exit 1; }
[ -d "$MODELS" ] || { echo "no $MODELS: set MODELS to the models folder"; exit 1; }
case "$MODELS" in /*) ;; *) echo "MODELS must be a full path"; exit 1 ;; esac

id llama >/dev/null 2>&1 || useradd --system --user-group --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin llama
for g in render video; do getent group "$g" >/dev/null && usermod -a -G "$g" llama; done
# whoever ran this with sudo reads llama-server's log without it, through Debian's systemd-journal
# group, which reads the journal and changes nothing (install.sh does the same)
reader=""
if [ -n "${SUDO_USER:-}" ] && [ "$SUDO_USER" != root ] && id "$SUDO_USER" >/dev/null 2>&1 && getent group systemd-journal >/dev/null; then
    id -nG "$SUDO_USER" | tr ' ' '\n' | grep -qx systemd-journal || reader="$SUDO_USER"
    usermod -a -G systemd-journal "$SUDO_USER"
fi

install -d -o root -g llama -m 0750 /etc/llama
presets="$here/presets.ini"
[ -f "$here/../private/presets.ini" ] && presets="$here/../private/presets.ini"   # our own, when it's here
# each file goes in only when it's different, and says so
place() { # place <new file> <where> <owner:group> <mode> <what it is>
    if cmp -s "$1" "$2"; then rm -f "$1"; return; fi
    chown "$3" "$1" && chmod "$4" "$1" && mv -f "$1" "$2"
    changed="${changed:+$changed, }$5"
}
sed "s#@MODELS@#$MODELS#g" "$presets" > /etc/llama/models.ini.new
place /etc/llama/models.ini.new /etc/llama/models.ini root:llama 0640 "the presets"
sed "s#@MODELS@#$MODELS#g" "$here/../systemd/llama-server.service" > /etc/systemd/system/llama-server.service.new
place /etc/systemd/system/llama-server.service.new /etc/systemd/system/llama-server.service root:root 0644 "the unit"
systemctl daemon-reload
systemctl enable llama-server.service
if [ -n "$changed" ]; then
    systemctl restart llama-server.service
    restarted="restarted for $changed"
elif ! systemctl is-active --quiet llama-server.service; then
    systemctl start llama-server.service
    restarted="started"
else
    restarted="unchanged, so left running with its model loaded"
fi

echo
echo "llama-server: $(/usr/local/bin/llama-server --version 2>&1 | head -n1), $restarted"
echo "  models: $MODELS, presets: /etc/llama/models.ini (from ${presets#$here/../})"
missing=""
while read -r path; do [ -f "$path" ] || missing="$missing $(basename "$path")"; done < <(sed -n 's/^\(model\|spec-draft-model\|lora\) = //p' /etc/llama/models.ini | sort -u)
[ -z "$missing" ] || echo "  not downloaded yet:$missing (models/fetch.sh)"
echo "  the list: curl -s http://127.0.0.1:8080/models"
echo "  follow it: journalctl -u llama-server -f"
[ -z "$reader" ] || echo "  added $reader to the systemd-journal group: log in again, and that needs no sudo"
