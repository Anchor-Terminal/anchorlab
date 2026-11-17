# anchorlab

The harness [Anchor Terminal](https://www.anchorterminal.com) uses to try the tools it lists. It
runs on a box of our own, has a local model write small Go programs that call a hosted MCP server's
tools, runs each program in a sandbox with no network, and writes down what happened: which tools
answered, on which attempt, how fast, and what it cost.

It's public so that anyone can see exactly how a tool is tested, and a vendor can run the same thing
against their own server and check our numbers. `DESIGN.md` has why it's built this way. This file
is how to build it, install it and run it.

It's early. The tests pass and the systemd units pass `systemd-analyze verify`, but the units
haven't had their first run on real hardware yet. The tasks so far are `connect` (open the server
and list its tools) and `explore` (can a model, given only what the server says about its tools,
get each one to answer?). Hosted MCP servers only, and only ones that need no key.

Go, standard library only. MIT licence.

## What it runs on, and why

One machine for now: a Minisforum MS-S1 Max, with an AMD Ryzen AI Max+ 395 (Strix Halo), 128 GB of
unified memory with about 100 GB of it given to the GPU, and 4 TB of disk, on Debian 13. It sits
beside the server that runs anchorterminal.com, on the same switch. It can reach the internet and
nothing else on the local network, and the only way in is from that server. The models run on it
with llama.cpp over Vulkan, one at a time; `MODELS.md` has which ones and how.

Why this machine: what decides which models a machine can run is how much memory its GPU can use,
and unified memory lets the GPU use most of the machine's. This was the cheapest machine with
128 GB of unified memory we could find, cheaper than any other unified-memory machine of that size
for running models locally. The trade is speed. Its memory bandwidth is 256 GB/s, so a dense model
reads all of itself for every token and is slow, and the mixture-of-experts models, which read only
their active part, are the ones that run well.

We plan to add machines as the lab earns its keep. The harness doesn't need changing for that: each
box runs its own copy, tests its own list and is collected the same way.

```
go test -short ./...   # a few seconds
go test ./...          # also compiles and runs real programs in the cell's code
```

## How a test runs

1. The harness opens the server under test and lists its tools. A tool the server marks
   destructive, or marks as not read-only, is never called. For a server that doesn't say, a tool
   named like a change (`create_`, `delete_`, `send_`, `submit_`, `pay_` and the rest in
   `gateway/gateway.go`) isn't either. Anchor Terminal's own `/mcp` marks all 23 of its tools: the
   lab would call the 13 read-only ones and leave the 10 that post, file, send or edit.
2. The model reads the tool list and replies with a Go program. The program can do one thing to
   the outside world: `tool.Call(name, args)`, which goes down a pipe to the harness.
3. The program is built and run in a cell: a systemd unit started for that one job, as a user made
   for it, with no network at all (not even loopback), the system read-only, and limits on memory,
   processes and time. The harness can't pass the cell an option, so a harness that's been talked
   into something can't weaken one.
4. The harness's gateway makes each call the program asks for, to the server under test and
   nowhere else (https, a public address checked at connect time, no redirects, no proxy, no
   credentials from the model), and counts and times it.
5. What the program printed goes back to the model, up to six replies. The outcome is arithmetic
   over the gateway's counts, never the model's opinion.

Everything is written down as it happens. Only numbers and words from fixed lists leave the box: no
tool description, no error message and nothing a model wrote. The transcripts stay on the box for a
person to read.

## What runs where

| Where | What | As |
| --- | --- | --- |
| The lab box | `anchor-lab run`, the harness (`anchor-lab.service`) | `anchorlab`, a user with no shell |
| The lab box | `anchor-lab cell`, one for each program (`anchor-lab-cell@.service`, started by the socket) | a user made for that run, no network |
| The lab box | `anchor-lab sync`, when the collecting server asks over SSH | `labsync`, by SSH key, this one command |
| The lab box | `anchor-lab stats`, numbers for an admin page (`anchor-lab-stats.service`) | a user made each start, read-only |
| Anywhere | `build.sh`; `anchor-lab targets`, the list of servers to test, from Anchor Terminal's public API | anyone |

The box needs Debian or another systemd distribution, Go (the cells compile the programs with it),
and a model served on the box itself in the OpenAI chat format. The harness refuses a model address
that isn't the box: the prompts hold strangers' text. `MODELS.md` sets that up with llama.cpp: the
GPU, the build, eight models, and one `llama-server` that loads whichever the harness asks for.

## Building

On the box itself, from a clone of this repository. It's public, so the clone needs no key, and
the box has Go for the cells anyway:

```
git clone https://github.com/Anchor-Terminal/anchorlab && cd anchorlab
./build.sh                          # with the public sample bench set
./build.sh path/to/private.go.txt   # with a bench set of your own (see "Picking the model")
```

It vets, runs the short tests and writes `./anchor-lab`. A private bench set is one file, copied
into `private/` on the box and named in `private/settings.env`, so `./build.sh` alone picks it up.
Building elsewhere and copying the binary over works too.

## The list of servers to test

The lab tests the tools Anchor Terminal lists, and takes the list from Anchor Terminal's public
API, the same `/api/v1/tools.json` anyone can read: every listing whose hosted MCP server answered
the site's own probe without a key (111 on 17 November 2026). Those need no account, so the lab
tests them with no credentials at all.

```
./anchor-lab targets > targets.json                   # from https://www.anchorterminal.com/api/v1/tools.json
./anchor-lab targets -from tools.json > targets.json  # from a copy saved earlier
```

It needs nothing but the internet, so it can run on the box itself. `targets.json`:

```json
{"v": "anchor-lab-targets/1", "made": "2026-11-17T00:00:00Z",
 "targets": [{"slug": "anchor-terminal", "name": "Anchor Terminal", "company": "anchorterminal.com",
              "url": "https://www.anchorterminal.com/mcp/connected"}]}
```

`company` is the company's id as the site has it, which decides where the lab files what it finds
(below). For your own server, write a list by hand. The example is us: Anchor Terminal's read-only
MCP server, which needs no key and has seven tools, all marked read-only.

## Putting it on the box

From the clone, after `./build.sh`, as root: `bash install.sh`. It makes the `anchorlab` and `labsync` users, installs the
binary and the units, and installs nothing else. It can be run again: it replaces the binary and the
units and leaves the state alone. It prints what's left to do by hand:

1. The model's name in `/etc/anchor-lab/env` (`ANCHOR_LAB_MODEL=`), as the model's server knows it
   (the names in `MODELS.md`). The harness speaks to `http://127.0.0.1:8080/v1` unless
   `ANCHOR_LAB_MODEL_URL` says another address on the same machine. It sends no temperature, so the
   server's setting for each model applies, unless `ANCHOR_LAB_TEMPERATURE` sets one for all. With no
   model, only `connect` runs, which is a fine first day.
2. The first list: `install -o anchorlab -g anchorlab -m 0640 targets.json /var/lib/anchor-lab/inbox/targets.json`,
   or make it on the box: `anchor-lab targets | sudo -u anchorlab tee /var/lib/anchor-lab/inbox/targets.json >/dev/null`.
3. For a server to collect results: its public key in `/var/lib/labsync/.ssh/authorized_keys`, on
   one line that starts `restrict,command="/usr/local/bin/anchor-lab sync" `, and `labsync` added
   to sshd's `AllowUsers`, key only.

## The first run

Stop the service and test one server by hand, watching the journal in a second terminal:

```
systemctl stop anchor-lab
sudo -u anchorlab anchor-lab run -once -only anchor-terminal -model <name>
tail -f /var/lib/anchor-lab/events/$(date -u +%F).ndjson
```

If a cell can't build or run, the event says why, and `journalctl -u 'anchor-lab-cell@*'` has the
cell's own words. The unit's system call filter is the first suspect: a refused call shows as
"operation not permitted" in the build output.

## Talking to the lab

`anchor-lab status` is the lab from a terminal: what it's testing now, today's counts, the machine,
and the latest results.

```
sudo anchor-lab status                            # on the box: reads the state itself
anchor-lab status -from http://<the box>:8790     # from the machine the box lets ask (the stats below)
anchor-lab status -watch                          # again every 10 seconds, until Ctrl-C
anchor-lab status -json                           # the stats as they are, for a script
```

```
anchor-lab 2026.11.17-1400 · model gpt-oss-120b · 61 targets · last event 40 s ago

now      anchor-terminal, exploring, turn 2, 4 calls (1 error), started 3 min ago
today    19 servers (16 opened, 3 didn't) · 15 explored: 6 every tool, 7 some, 2 none
         142 calls (21 errors) · 50 programs (7 didn't build) · model 41 min, 445,000 tokens
machine  load 3.42 2.91 2.10 · memory 39 GB of 124 GB free · GPU 91% 64°C 118W, GTT 67 GB of 100 GB
         / 82 GB of 98 GB free · /var 467 GB of 492 GB free · up 5.1 h

latest (38 kept on the box)
  ended             listing          task                    status  outcome  tools      calls      p50/p95
  2026-11-17 13:07  anchor-terminal  explore · gpt-oss-120b  ok      partial  5/6 of 7   11 (3 err) 240/900 ms
```

`ANCHOR_LAB_STATS_URL` set to the box's address saves typing `-from` each time. `tail -f` on
today's events file is the raw view of the same thing.

## The admin page's numbers

`anchor-lab stats` answers `GET /v1/stats` on the box's own address, to the addresses you allow and
nobody else: the run in progress, today's counts, the latest twenty results, and the machine (load,
memory, disks, the GPU). It reads and writes nothing else, and refuses to listen on every
interface. In `/etc/anchor-lab/env`:

```
ANCHOR_LAB_STATS_LISTEN=<the box's address>:8790
ANCHOR_LAB_STATS_ALLOW=<the address that may ask>
```

Run `install.sh` again: it turns `anchor-lab-stats` on and prints the firewall line to add, which
lets only that address reach the port. The format is `anchor-lab-stats/1` (`stats/stats.go`).

## Picking the model

The bench puts a model through known-answer cases on made-up servers that run inside the harness.
It reaches no network, and its runs stay out of the results that leave the box.

```
sudo -u anchorlab anchor-lab bench -model gpt-oss-120b
sudo -u anchorlab anchor-lab bench -model gemma-4-31b
sudo -u anchorlab anchor-lab bench -compare
```

`MODELS.md`, "Picking one", runs all eight in one go.

This repository has a sample set (`bench/set_sample.go`): ten tools on two servers, each kind of
trouble at least once. The fifty Anchor Terminal picks its model with are kept private, because a
public set of known answers is one a model can be tuned to. To bench on cases nobody has seen, write
your own the same way: a file that starts `//go:build privatebench`, in `package bench`, defining
`Version`, `Cases` and `Right`, and build with `./build.sh <your file>`. A `_test.go.txt` file beside
it with the same name runs with the build.

## The research folder

What the lab finds is filed by model, then company, then tool:
`research/<model>/<company>/<tool>/<run>/`, the same company-then-tool order as Anchor Terminal's own
research, with the model on top. One model's work can be read, compared with another's or deleted
as a whole, and every run against one company's tools sits together.

```
research/gpt-oss-120b/anchorterminal.com/anchor-terminal/20261117T150000-anchor-terminal/
research/gpt-oss-120b/exa.ai/exa-mcp/20261117T151200-exa-mcp/
research/gemma-4-26b-a4b/exa.ai/exa-mcp/20261118T090000-exa-mcp/
research/no-model/...                     connect-only runs, with no model set
```

On the box it's under `/var/lib/anchor-lab/`. Copied off the box it goes in `private/research/`,
the same layout, never public: the transcripts hold servers' tool descriptions and models' words.

## Switching the model

```
anchor-lab model                       # the model the lab uses, and every model the server offers
sudo anchor-lab model use gemma-4-31b  # switch: checks the server has it, saves it, restarts the lab
sudo anchor-lab model use none         # connect only
```

`use` checks the name against what the model server offers, writes it as `ANCHOR_LAB_MODEL` in
`/etc/anchor-lab/env`, asks the server to load it and restarts `anchor-lab`. From then on new runs
are filed under the new model's folder.

## The private folder

`private/` is ignored by git except for its README and an example settings file. It's where our own
things go, next to the code but never in the public repository: results and transcripts copied off
the box (`private/research/`), `private/settings.env` (the bench set's path, the models folder),
our own model presets,
notes. `build.sh`, `models/fetch.sh` and `models/install.sh` read `private/settings.env` when it's
there, and `models/install.sh` uses `private/presets.ini` in place of `models/presets.ini` when that
is. Why there's a private folder in a public repository at all: one checkout holds both, and the
scripts find our settings without anything about our machines being written into the code.
`private/README.md` says what goes where.

## The files on the box

```
/var/lib/anchor-lab/events/<day>.ndjson   every event as it happens
/var/lib/anchor-lab/research/<model>/<company>/<tool>/<run>/
                                          tools.json, model-N.txt, code-N.go, output-N.txt
/var/lib/anchor-lab/results.ndjson        one line a finished task; what a collecting server takes
/var/lib/anchor-lab/inbox/targets.json    the servers to test
/var/lib/anchor-lab/bench.ndjson          every model's bench score
/var/lib/anchor-lab/bench/                the bench's own events and runs, kept apart
```

A line of `results.ndjson` (format `anchor-lab-result/1`, `journal/journal.go`), with made-up
numbers and some fields left out:

```
{"v":"anchor-lab-result/1","run":"20261117T021400-anchor-terminal","slug":"anchor-terminal","task":"explore","model":"gpt-oss-120b",
 "status":"ok","tools":7,"outcome":"partial","calls":11,"errors":3,"p50Ms":240,"p95Ms":900,"toolsTried":6,"toolsOk":5,
 "turns":4,"builds":4,"buildFails":1,"perTool":[{"name":"search_tools","calls":2,"ok":1,"errors":1,"firstOk":2,"medianMs":210}]}
```

## Never

- `-unsafe-cells` on the box. It runs programs as plain children of the harness, for working on the
  harness on a machine with nothing to lose.
- A model address that isn't the box itself. The harness refuses it.
- A key, a token or a checkout of anything private on the box. A clone of this public repository is
  fine; the private bench set is one file in `private/`, which git ignores.

## Reporting a problem

A way out of a cell, or a way to make the gateway call somewhere it shouldn't, is a security
problem: email security@anchorterminal.com rather than opening an issue. Anything else, an issue is
fine.
