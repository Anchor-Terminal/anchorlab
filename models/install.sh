#!/usr/bin/env bash
# models/install.sh: set up llama-server for the lab's models. Run as root, after llama.cpp is built
# and installed (MODELS.md, "llama.cpp"). Safe to run again: it replaces the presets and the unit.
#
#   MODELS=/srv/models bash models/install.sh
#
# Makes the user `llama` (no shell, no home, in the render and video groups for the GPU), puts
# presets.ini in /etc/llama/models.ini and the unit in /etc/systemd/system/, and starts it.
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
# our own settings, when they're here (private/README.md); MODELS from the environment wins
if [ -f "$here/../private/settings.env" ]; then
    keep="${MODELS:-}"; set -a; . "$here/../private/settings.env"; set +a; [ -z "$keep" ] || MODELS="$keep"
fi
MODELS="${MODELS:-/srv/models}"
[ "$(id -u)" = 0 ] || { echo "run as root"; exit 1; }
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
sed "s#@MODELS@#$MODELS#g" "$presets" > /etc/llama/models.ini.new
chown root:llama /etc/llama/models.ini.new && chmod 0640 /etc/llama/models.ini.new
mv -f /etc/llama/models.ini.new /etc/llama/models.ini
sed "s#@MODELS@#$MODELS#g" "$here/../systemd/llama-server.service" > /etc/systemd/system/llama-server.service
chmod 0644 /etc/systemd/system/llama-server.service
systemctl daemon-reload
systemctl enable llama-server.service
systemctl restart llama-server.service

echo
echo "llama-server: $(/usr/local/bin/llama-server --version 2>&1 | head -n1)"
echo "  models: $MODELS, presets: /etc/llama/models.ini (from ${presets#$here/../})"
missing=""
while read -r path; do [ -f "$path" ] || missing="$missing $(basename "$path")"; done < <(sed -n 's/^model = //p' /etc/llama/models.ini)
[ -z "$missing" ] || echo "  not downloaded yet:$missing (models/fetch.sh)"
echo "  the list: curl -s http://127.0.0.1:8080/models"
echo "  follow it: journalctl -u llama-server -f"
[ -z "$reader" ] || echo "  added $reader to the systemd-journal group: log in again, and that needs no sudo"
