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

```
go test -short ./...   # a few seconds
go test ./...          # also compiles and runs real programs in the cell's code
```

## How a test runs

1. The harness opens the server under test and lists its tools. Tools named like a change
   (`create_`, `delete_`, `send_`, `pay_` and the rest in `gateway/gateway.go`), or marked
   destructive, are never called.
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
| The collecting server | `build.sh`, and `anchor-lab targets` to write the list of servers to test | anyone |

The box needs Debian or another systemd distribution, Go (the cells compile the programs with it),
and a model served on the box itself in the OpenAI chat format (llama.cpp's `llama-server` does).
The harness refuses a model address that isn't the box: the prompts hold strangers' text.

## Building

On the machine that will send it to the box:

```
./build.sh                          # with the public sample bench set
./build.sh path/to/private.go.txt   # with a bench set of your own (see "Picking the model")
```

It vets, runs the short tests and writes `./anchor-lab`. The box gets a binary, never a checkout.

## The list of servers to test

`targets.json`:

```json
{"v": "anchor-lab-targets/1", "made": "2026-11-17T00:00:00Z",
 "targets": [{"slug": "exa-mcp", "name": "Exa", "url": "https://mcp.exa.ai/mcp"}]}
```

Anchor Terminal makes it from its listings with
`./anchor-lab targets -data <the site's data folder> -snapshot <its live snapshot> > targets.json`.
For your own server, write one by hand.

## Putting it on the box

Copy `anchor-lab`, `install.sh`, `systemd/` and `targets.json` to the box, and there, as root, run
`install.sh` from the folder they're in. It makes the `anchorlab` and `labsync` users, installs the
binary and the units, and installs nothing else. It can be run again: it replaces the binary and the
units and leaves the state alone. It prints what's left to do by hand:

1. The model's name in `/etc/anchor-lab/env` (`ANCHOR_LAB_MODEL=`), as the model's server knows it.
   The harness speaks to `http://127.0.0.1:8080/v1` unless `ANCHOR_LAB_MODEL_URL` says another
   address on the same machine. With no model, only `connect` runs, which is a fine first day.
2. The first list: `install -o anchorlab -g anchorlab -m 0640 targets.json /var/lib/anchor-lab/inbox/targets.json`.
3. For a server to collect results: its public key in `/var/lib/labsync/.ssh/authorized_keys`, on
   one line that starts `restrict,command="/usr/local/bin/anchor-lab sync" `, and `labsync` added
   to sshd's `AllowUsers`, key only.

## The first run

Stop the service and test one server by hand, watching the journal in a second terminal:

```
systemctl stop anchor-lab
sudo -u anchorlab anchor-lab run -once -only exa-mcp -model <name>
tail -f /var/lib/anchor-lab/events/$(date -u +%F).ndjson
```

If a cell can't build or run, the event says why, and `journalctl -u 'anchor-lab-cell@*'` has the
cell's own words. The unit's system call filter is the first suspect: a refused call shows as
"operation not permitted" in the build output.

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

This repository has a sample set (`bench/set_sample.go`): ten tools on two servers, each kind of
trouble at least once. The fifty Anchor Terminal picks its model with are kept private, because a
public set of known answers is one a model can be tuned to. To bench on cases nobody has seen, write
your own the same way: a file that starts `//go:build privatebench`, in `package bench`, defining
`Version`, `Cases` and `Right`, and build with `./build.sh <your file>`. A `_test.go.txt` file beside
it with the same name runs with the build.

## The files on the box

```
/var/lib/anchor-lab/events/<day>.ndjson   every event as it happens
/var/lib/anchor-lab/runs/<run>/           tools.json, model-N.txt, code-N.go, output-N.txt
/var/lib/anchor-lab/results.ndjson        one line a finished task; what a collecting server takes
/var/lib/anchor-lab/inbox/targets.json    the servers to test
/var/lib/anchor-lab/bench.ndjson          every model's bench score
/var/lib/anchor-lab/bench/                the bench's own events and runs, kept apart
```

A line of `results.ndjson` (format `anchor-lab-result/1`, `journal/journal.go`), with made-up
numbers and some fields left out:

```
{"v":"anchor-lab-result/1","run":"20261115T021400-exa-mcp","slug":"exa-mcp","task":"explore","model":"gpt-oss-120b",
 "status":"ok","tools":4,"outcome":"partial","calls":9,"errors":3,"p50Ms":640,"p95Ms":2100,"toolsTried":4,"toolsOk":3,
 "turns":4,"builds":4,"buildFails":1,"perTool":[{"name":"web_search_exa","calls":2,"ok":1,"errors":1,"firstOk":2,"medianMs":610}]}
```

## Never

- `-unsafe-cells` on the box. It runs programs as plain children of the harness, for working on the
  harness on a machine with nothing to lose.
- A model address that isn't the box itself. The harness refuses it.
- A key, a token or a checkout of anything on the box.

## Reporting a problem

A way out of a cell, or a way to make the gateway call somewhere it shouldn't, is a security
problem: email security@anchorterminal.com rather than opening an issue. Anything else, an issue is
fine.
