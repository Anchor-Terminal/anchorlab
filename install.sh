#!/usr/bin/env bash
# install.sh: put the lab on the lab box. Run as root, on the box, from a folder that holds
# this script, the anchor-lab binary and the systemd folder (README.md says how they get there).
# Safe to run again: it replaces the binary and the units and leaves the state alone.
#
# It makes two users that can't do anything else:
#   anchorlab   runs the harness; owns /var/lib/anchor-lab; no shell, no home, no sudo
#   labsync     what the site's server logs in as, by key only, tied to `anchor-lab sync`
# It removes agent-run, the wrapper the cells replace, when it's on PATH. It changes nothing about
# the firewall or sshd: what to add to those is printed at the end.
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
[ "$(id -u)" = 0 ] || { echo "run as root"; exit 1; }
[ -x "$here/anchor-lab" ] || { echo "no anchor-lab binary beside this script"; exit 1; }
command -v go >/dev/null || { echo "no go on PATH: the cells compile the programs with it"; exit 1; }

id anchorlab >/dev/null 2>&1 || useradd --system --user-group --no-create-home --home-dir /nonexistent --shell /usr/sbin/nologin anchorlab
id labsync >/dev/null 2>&1 || useradd --system --create-home --home-dir /var/lib/labsync --shell /bin/sh --groups anchorlab labsync
usermod -a -G anchorlab labsync
# whoever ran this with sudo runs the lab from now on without it: reading the state, switching the
# model and asking for a bench all go through what the anchorlab group may read and write
operator=""
if [ -n "${SUDO_USER:-}" ] && [ "$SUDO_USER" != root ] && id "$SUDO_USER" >/dev/null 2>&1; then
    id -nG "$SUDO_USER" | tr ' ' '\n' | grep -qx anchorlab || operator="$SUDO_USER"
    usermod -a -G anchorlab "$SUDO_USER"
fi

install -o root -g root -m 0755 "$here/anchor-lab" /usr/local/bin/anchor-lab.new
mv -f /usr/local/bin/anchor-lab.new /usr/local/bin/anchor-lab
install -d -o anchorlab -g anchorlab -m 0750 /var/lib/anchor-lab
install -d -o anchorlab -g anchorlab -m 2770 /var/lib/anchor-lab/inbox
install -d -o root -g anchorlab -m 0750 /etc/anchor-lab
[ -f /etc/anchor-lab/env ] || install -o root -g anchorlab -m 0640 /dev/stdin /etc/anchor-lab/env <<'ENV'
# The model the harness asks, served on this machine in the OpenAI chat format (llama.cpp's
# llama-server does). Leave ANCHOR_LAB_MODEL empty and only the connect task runs.
ANCHOR_LAB_MODEL=
ANCHOR_LAB_MODEL_URL=http://127.0.0.1:8080/v1
ENV
# Empty sends no temperature, so the model server's preset for the model applies (MODELS.md).
grep -q '^ANCHOR_LAB_TEMPERATURE=' /etc/anchor-lab/env || cat >>/etc/anchor-lab/env <<'ENV'
# A temperature for every model, or empty for each model's own from the server's presets.
ANCHOR_LAB_TEMPERATURE=
ENV
# The stats for the site's admin page (anchor-lab-stats.service): the box's own address and a port,
# and the site's server's address. Left empty, the stats service stays off. Added to an env file
# from before it existed.
grep -q '^ANCHOR_LAB_STATS_LISTEN=' /etc/anchor-lab/env || cat >>/etc/anchor-lab/env <<'ENV'
# The admin page's view of the lab: the box's address and port, and who may ask (the site's server).
ANCHOR_LAB_STATS_LISTEN=
ANCHOR_LAB_STATS_ALLOW=
ENV
install -d -o labsync -g labsync -m 0700 /var/lib/labsync/.ssh
[ -f /var/lib/labsync/.ssh/authorized_keys ] || install -o labsync -g labsync -m 0600 /dev/null /var/lib/labsync/.ssh/authorized_keys

for u in anchor-lab-cell.socket anchor-lab-cell@.service anchor-lab.service anchor-lab-stats.service; do
    install -o root -g root -m 0644 "$here/systemd/$u" "/etc/systemd/system/$u"
done
systemctl daemon-reload
systemctl enable --now anchor-lab-cell.socket
systemctl enable anchor-lab.service
systemctl restart anchor-lab.service
listen="$(sed -n 's/^ANCHOR_LAB_STATS_LISTEN=//p' /etc/anchor-lab/env | tail -n1)"
allow="$(sed -n 's/^ANCHOR_LAB_STATS_ALLOW=//p' /etc/anchor-lab/env | tail -n1)"
if [ -n "$listen" ] && [ -n "$allow" ]; then
    systemctl enable anchor-lab-stats.service
    systemctl restart anchor-lab-stats.service
else
    systemctl disable --now anchor-lab-stats.service 2>/dev/null || true
fi

# The cells replace agent-run, the wrapper that started a sandbox by hand for each agent
# (DESIGN.md: "yes replace"). The wrapper goes, wherever on PATH it is; what earlier
# agents wrote stays. Only a full path to a plain file is removed.
retired=""
for old in $(type -aP agent-run 2>/dev/null || true); do
    case "$old" in
        /*) if [ -f "$old" ]; then rm -f -- "$old"; retired="$retired $old"; fi ;;
    esac
done
rules="$(grep -ls 'agent-run' /etc/sudoers.d/* 2>/dev/null || true)"

echo
echo "installed $(/usr/local/bin/anchor-lab version)"
[ -z "$retired" ] || echo "  removed agent-run ($(echo $retired)): the cells do its job. /var/lib/agents is left as it was"
[ -z "$rules" ] || echo "  a sudo rule still names agent-run, for you to take out: $(echo $rules)"
echo "  follow it:   tail -f /var/lib/anchor-lab/events/\$(date -u +%F).ndjson    (or journalctl -u anchor-lab -f)"
echo "  how it goes: anchor-lab status   (no sudo once you're in the anchorlab group)"
[ -z "$operator" ] || echo "  added $operator to the anchorlab group: log in again, and the lab needs no sudo from then on"
echo
echo "Still to do by hand, once (README.md has each line):"
echo "  1. a model: anchor-lab model use <name> (MODELS.md has the names)"
echo "  2. the site server's public key in /var/lib/labsync/.ssh/authorized_keys, on one line, starting"
echo "     restrict,command=\"/usr/local/bin/anchor-lab sync\" "
echo "  3. labsync added to AllowUsers in sshd's config, key only"
echo "  4. a list of targets: anchor-lab targets -inbox /var/lib/anchor-lab (from Anchor Terminal's public API)"
if [ -n "$listen" ] && [ -n "$allow" ]; then
    echo "  5. stats on $listen for $allow: the firewall's input chain needs, before its drop,"
    echo "     ip saddr ${allow%%,*} tcp dport ${listen##*:} accept"
else
    echo "  5. for the admin page: ANCHOR_LAB_STATS_LISTEN and ANCHOR_LAB_STATS_ALLOW in /etc/anchor-lab/env,"
    echo "     then run this again (it turns the stats service on and prints the firewall line)"
fi
