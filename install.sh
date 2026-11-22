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
# and reading the logs (journalctl -u anchor-lab, the cells', llama-server's) goes through Debian's
# systemd-journal group, which reads the journal and changes nothing
operator=""
if [ -n "${SUDO_USER:-}" ] && [ "$SUDO_USER" != root ] && id "$SUDO_USER" >/dev/null 2>&1; then
    for g in anchorlab systemd-journal; do
        getent group "$g" >/dev/null || continue
        id -nG "$SUDO_USER" | tr ' ' '\n' | grep -qx "$g" || operator="$SUDO_USER"
        usermod -a -G "$g" "$SUDO_USER"
    done
fi

install -o root -g root -m 0755 "$here/anchor-lab" /usr/local/bin/anchor-lab.new
mv -f /usr/local/bin/anchor-lab.new /usr/local/bin/anchor-lab
install -d -o anchorlab -g anchorlab -m 0750 /var/lib/anchor-lab
install -d -o anchorlab -g anchorlab -m 2770 /var/lib/anchor-lab/inbox
install -d -o anchorlab -g anchorlab -m 2770 /var/lib/anchor-lab/inbox/keys
install -d -o root -g anchorlab -m 0750 /etc/anchor-lab
# The box's key pair (package seal): the private half readable by root alone and handed to the
# harness's service by systemd (LoadCredential), the public half for the stats to give out, so the
# admin's browser can seal a server's key to it. Both in root's folder: nobody but root can change
# which key the site seals to. Made once; a box that has one keeps it.
/usr/local/bin/anchor-lab boxkey -key /etc/anchor-lab/box.key -pub /etc/anchor-lab/box.pub
chown root:root /etc/anchor-lab/box.key && chmod 0600 /etc/anchor-lab/box.key
chown root:anchorlab /etc/anchor-lab/box.pub && chmod 0640 /etc/anchor-lab/box.pub
rm -f /var/lib/anchor-lab/box.pub # where an earlier build of this change put it
# The secret the site signs its commands with, readable by root alone and handed to the stats
# service by systemd. The site's server needs the same, as ANCHOR_LAB_CONTROL_SECRET.
newsecret=""
if [ ! -s /etc/anchor-lab/control.secret ]; then
    (umask 077; od -An -tx1 -N32 /dev/urandom | tr -d ' \n' >/etc/anchor-lab/control.secret)
    newsecret=1
fi
chown root:root /etc/anchor-lab/control.secret && chmod 0600 /etc/anchor-lab/control.secret
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
# The harness asks the model server what context each model has, and uses this much of it at most
# (MODELS.md, "The context").
grep -q '^ANCHOR_LAB_CONTEXT=' /etc/anchor-lab/env || cat >>/etc/anchor-lab/env <<'ENV'
# The most of a model's context the lab uses, in tokens. Empty is 131072.
ANCHOR_LAB_CONTEXT=
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
echo "  the box's key: $(/usr/local/bin/anchor-lab boxkey -key /etc/anchor-lab/box.key -pub /etc/anchor-lab/box.pub | sed 's/.*: //')"
echo "    the admin page's Lab tab shows the same fingerprint beside \"Keys we hold\": check they match"
[ -z "$operator" ] || echo "  added $operator to the anchorlab and systemd-journal groups: log in again, and the lab and its logs need no sudo from then on"
echo
echo "Still to do by hand, once (README.md has each line):"
echo "  1. a model: anchor-lab model use <name> (MODELS.md has the names)"
echo "  2. a list of targets: anchor-lab targets -inbox /var/lib/anchor-lab (from Anchor Terminal's public API; deploy.sh does it each time)"
if [ -n "$newsecret" ]; then
    echo "  3. a new command secret: the site's server needs the same one, as"
    echo "     ANCHOR_LAB_CONTROL_SECRET=<it> in its env file. Read it with: sudo cat /etc/anchor-lab/control.secret"
fi
if [ -n "$listen" ] && [ -n "$allow" ]; then
    echo "  4. stats on $listen for $allow: the firewall's input chain needs, before its drop,"
    echo "     ip saddr ${allow%%,*} tcp dport ${listen##*:} accept"
else
    echo "  4. for the admin page: ANCHOR_LAB_STATS_LISTEN and ANCHOR_LAB_STATS_ALLOW in /etc/anchor-lab/env,"
    echo "     then run this again (it turns the stats service on and prints the firewall line)"
fi
