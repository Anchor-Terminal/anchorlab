# anchorlab: the design

Why the harness is built the way it is. README.md is how to run it. "The site" is anchorterminal.com,
which grades the tools agents use and collects the lab's results.

## What it's for

Every grade on the site comes from reading: documentation, prices, terms, source, status pages. The
benchmark's Performance and Task success categories are still pending because nothing of ours has
used the tools. The lab is the machine that does. It sits beside the
site's server, runs open-weight models, and has agents try the tools we list and write down what
happened.

The box runs plain Debian. It can reach the internet and nothing on the local network, and the only
way in is SSH from the site's server. The
design leans on that and adds its own walls inside the box, so no single one of them has to hold.

## Who is trusted

Nothing the lab reads is. In order of how little:

1. **The tool under test.** Its descriptions and answers are a stranger's text, and some will be
   written to steer the model that reads them.
2. **The model.** It has just read that text. Whatever it writes may be the stranger's idea.
3. **The program the model wrote.** It's run, so it gets the smallest room in the building.
4. **The lab's results**, as the site's server sees them. A box that runs all of the above is
   assumed to be lying until its lines have been checked one field at a time.

The harness, the gateway and the systemd units are ours and are the only things trusted. They're
small, they use the standard library only, and they treat everything above as data.

## Decisions

- **The model writes programs, and the programs can only ask.** Each test is a conversation: the
  model reads the server's tool list and replies with a small Go program, the harness runs it, and
  what it printed goes back to the model. The program runs in a cell with no network at all. Its
  one way out is `tool.Call(name, args)`, which goes down a pipe to the harness, and the harness's
  gateway decides whether to make the call. So the code that's least trusted never holds a socket,
  and everything a run does to a vendor's server passes through one place that counts and limits
  it.
- **Go for the programs.** The toolchain is already on the box, so nothing is installed for it.
  The compiler catches a careless program before it runs, and a build from an empty cache took 5
  seconds on two cores here. The protocol between the harness and a cell doesn't care about the
  language, so Python or JavaScript can be added as a second kind of job if the local models turn
  out to write Go badly. It's still the first thing to look
  at if the first runs show mostly compile errors.
- **A cell is a systemd unit, and its walls are a file root installed.** The harness connects to a
  socket and systemd starts `anchor-lab-cell@.service` for that connection: a user made for the
  run, no network (not even loopback, where the model listens), the system read-only with /home,
  /var and the lab's own files hidden, a private /tmp in memory, and limits on memory, processes,
  CPU and time. The harness can't pass a cell an option, so a harness that's been talked into
  something can't weaken one. Nothing needs sudo while the lab runs, the harness included.
- **Every job starts with an empty build cache.** A shared cache would let one run leave a compiled
  package for the next run to link in. Five seconds a build buys that off.
- **The gateway calls one address a run, and only a public one.** The address comes from the list
  the site's server sent. It has to be https and a name. When the connection is made the address
  it resolved to is checked: this machine, private and link-local networks, carrier NAT, and IPv6
  forms that wrap any of those are refused, whatever DNS or a redirect says. Redirects aren't
  followed. The environment's proxy is never used. No header, cookie or credential comes from the
  model or the program.
- **Tools that change something are never called.** One the server marks destructive, or marks
  as not read-only (`readOnlyHint: false`), is listed in the result as not called, and so is one
  named like a change (`create_`, `delete_`, `send_`, `submit_`, `pay_` and the rest of the list
  in `gateway.go`) on a server that doesn't say. One the server marks read-only is called whatever
  its name. Trying the harness on Anchor Terminal's own `/mcp` is what showed the second rule was
  missing: `submit_review`, `file_issue`, `contact` and `react_chat` are marked as not read-only,
  aren't marked destructive, and have names the list didn't catch, so they would have been
  called. This costs coverage, and it's the right way round for calling other people's production
  servers without asking.
- **A server that wants a key to run its tools is the server's finding, not the model's failure.**
  The list of servers to test is the ones that list their tools without a key, and many of those
  then answer every call with 401. On the box's first evening, 199 of 204 HTTP errors were 401s,
  and three servers spent their whole 40-call budget on them. So a 401 or 403 is its own call
  status, `auth`; after two of them with nothing that worked, the gateway stops sending that server
  calls for the run and tells the program why; and a run where every call came back that way has
  status `auth` and outcome `none`, not `failure`.
- **The program sees what the server said.** A tool's error text and the body of an HTTP error
  come back in the reply's `Error`, which the SDK documents as why the call failed. Before, `Error`
  held a fixed phrase and the text sat in `Text`, so a model printing `Error` never saw "missing
  required field" and couldn't fix its arguments. The text is a stranger's and stays on the box,
  like every tool description the model already reads.
- **The numbers are the gateway's, never the model's.** Calls, errors, latency and which tool
  answered on which attempt are counted where the calls are made. The outcome is arithmetic over
  those (every callable tool answered without an error at least once: success; some: partial;
  none: failure). The model is asked to try, not to judge.
- **Two tasks to start.** `connect` opens the server and lists its tools, with no model. `explore`
  asks the question an agent has on its first day: given only what the server says about its
  tools, can I call them? It measures something the site can't read off a page: how many tries a
  model needs to get each tool to answer, and how fast the server is when it does.
- **The model is on the box and the prompts stay there.** The harness refuses a model address that
  isn't this machine.
- **One machine with unified memory, more as it pays.** A Ryzen AI Max+ 395 with 128 GB, about 100
  GB of it the GPU's: the cheapest unified-memory machine of that size we could find. Bandwidth is
  what it gives up, so mixture-of-experts models are the ones it runs fast. More machines come as the
  lab earns its keep; each runs its own copy of the harness.
- **The model's settings are the model's.** One `llama-server` in router mode serves every model the
  lab may use and loads the one a request names, so the bench can go through them all without a
  restart. Each model's sampling is its publisher's recommendation, in the server's presets
  (`models/presets.ini`), and the harness sends no temperature unless `ANCHOR_LAB_TEMPERATURE` sets
  one. It used to send 0.2 for every model, which overrode what every one of the eight publishers
  recommends (about 1.0) and is low enough to make a thinking model loop. Thinking is capped at
  2,048 tokens so a model that thinks still has room in a 4,096-token reply for the program.
- **The model files are pinned.** `models/models.txt` has each file's size and SHA-256 as the Hub
  gave it, and `models/fetch.sh` refuses a file that doesn't match. A publisher replacing a file is
  something to look at, not something to pick up quietly.
- **Debian first.** Everything on the box comes from Debian's own archive, trixie and then
  trixie-backports, unless there's no way round it. One source of packages means one place
  security updates come from, one key that signs them, and nothing that updates itself behind
  apt's back. So: Go, the kernel, the GPU firmware and Mesa are Debian's (the last three from
  backports, for this chip), and no third-party apt repository, install script, pip, npm, snap or
  downloaded binary goes on the box. Two things come from elsewhere because they have to:
  - **llama.cpp, built from its source.** Debian packages it only in testing and unstable, and that
    build is too old for what the lab uses (router mode, presets, the newest models). It's built
    with Debian's compiler against Debian's Vulkan libraries, and the day trixie-backports carries
    a recent enough llama.cpp, the source build goes.
  - **The model files, from Hugging Face.** Debian doesn't package models. Each file is pinned by
    its SHA-256 in `models/models.txt`, so what's downloaded is exactly what was checked.
- **The box builds from a clone of this repository.** It's public, so cloning it needs no key, and
  the box already has Go for the cells. That puts updating the lab on the box (`git pull`,
  `./build.sh`, `install.sh`) with nothing copied from the site's server but the private bench set,
  one file in `private/`. Earlier the box only ever got a binary built elsewhere; that was about
  keeping our private repository and its keys off the box, which this still does.
- **Our own things live in `private/`, which git ignores.** Results and transcripts copied off the
  box, our settings and presets, notes. The scripts read `private/settings.env` when it's there, so
  nothing about our machines is written into the public code, and one checkout holds both.
- **A terminal can ask the lab how it's going.** `anchor-lab status` reads the state on the box,
  or asks `anchor-lab stats` from the machine it lets ask, and prints the run in progress, today's
  counts, the machine and the latest results. It's the same numbers as the admin page, so nothing
  new leaves the box.
- **Everything is written down as it happens.** One file of events a day, appended and synced a
  line at a time, so `tail -f` is the lab's live view. The programs, their output and the model's
  replies are kept in a folder a run, filed by model, company and tool (below). One line a finished
  run goes to `results.ndjson`.
- **Only numbers and fixed words leave the box.** A result line has counts, timings, statuses from
  fixed lists, and the server's own tool names when they're shaped like names. No description, no
  error message, no sentence from a model. That keeps a stranger's text off the site and out of any
  prompt on the site's side, which is the rule reviews already follow . The transcripts stay on the box for a person to read.
- **The site's server pulls. The box never pushes.** The box can't reach the site's server, and it
  holds no key, token or private checkout of ours. The site's server has one SSH key on the box, for a user
  that can run one command (`anchor-lab sync`), which answers two requests: the result lines after
  a line number, and a new list of targets on standard input. Neither runs anything.
- **Every run records what it cost.** An explore result
  carries the model's time, its prompt and output tokens as the model's server counted them, and
  the cells' time building and running programs. The model is local, so the cost is time and
  power, not a bill. Tokens make it comparable with what a hosted model would charge.
- **Known-answer cases pick the model, and the real ones are private.** `anchor-lab bench -model
  <name>` puts a model through made-up servers. Our set has fifty tools on five servers. 43 can be
  made to answer, each with a right answer kept beside it in `bench.Right`, and
  each with the trouble real servers give: a date format, an id that's a string and not a number, a
  field the schema forgot to mark as required that only the error names, a cursor or an id that
  only another tool's answer gives. 7 must be left alone: named like a change, marked destructive,
  or carrying an instruction in the description. The score is how many of the 43 answered, on
  which attempt, how many of the 7 it tried, and what it cost. `anchor-lab bench -compare` ranks
  every model scored so far. Switching to the winner is a person editing `/etc/anchor-lab/env`.
  The fifty aren't in this repository: a public set of known answers is one a model can be tuned
  to, and then its score says nothing. The repository has a sample of ten (`bench/set_sample.go`)
  with each kind of trouble, and a private set is a file built in with `-tags privatebench`
  (`build.sh`). The private set's version is what a score is compared under.
- **A bench makes three passes, and a score says which cases changed.** A model answers the same
  case differently from one pass to the next, so one pass can rank two models by luck. `anchor-lab
  bench` goes through the set three times (`bench.DefaultRuns`, `-runs` for one run by hand): a case
  counts as answered when it answers in two passes of the three, steady when it answers in all, and
  flaky when it answers in some, which is its own finding. A trap tried in any pass counts, and the
  tokens and time are one pass's, so a three-pass score compares with an older one-pass score
  (read as one pass). `anchor-lab bench -flips a,b` lists the cases two models disagree on, since
  two models with the same total rarely answer the same cases and the ones that changed say more
  than the total. The reason, from Anthropic's article on folding subagents into one agent loop
  (22 October 2026): when you compare two set-ups, look at which tasks flipped, and run each task
  more than once, since a break there showed in one run of two. It costs three times the model's
  time on the bench.
- **The bench reaches no network.** Its servers run inside the harness and its sessions answer from
  them in memory (`gateway.OpenWith`, which only the bench may call, and a test holds the module to
  that). Its runs go to a journal of their own under `bench/`, so they never reach `results.ndjson`
  or the site. The programs still run in real cells, so the cell is tested too.
- **The bench is our own cases, not real servers.** Real servers change, rate-limit and go down,
  and a model's score shouldn't move because a vendor's did. Real servers are what `explore`
  measures. The bench measures the model.
- **The lab is a repository of its own, and public.** It imports nothing from the site and the site
  imports nothing from it, so the binary on the box has none of the site's code in it. The site's
  collector is written against the formats (`anchor-lab-result/1`, `anchor-lab-stats/1`), not
  against this code. It's public so that how a tool is tested can be read, and a vendor can run it
  against their own server.
- **The targets come from Anchor Terminal's public API.** `anchor-lab targets` reads
  `/api/v1/tools.json` and keeps the listings whose hosted MCP server answered the site's own probe
  without a key. That's the set where no account is needed, so the first version carries no
  credentials at all. Reading the public API, not the site's files, means anyone can make the same
  list, and the box can make it itself: it's the internet, which the box can reach, and nothing of
  ours it would need a key for.
- **Research is filed by model, then company, then tool.** Each run's files go in
  `research/<model>/<company>/<tool>/<run>/`: the company-then-tool order Anchor Terminal's own
  research uses, with the model on top, so one model's work can be compared with another's or
  dropped as a whole. The company comes from the list (`company`, which the API carries). Every
  part of the path is cut down to letters, digits, dots, dashes and underscores, so no name from a
  list or a setting reaches outside the folder.
- **Switching models is one command.** `anchor-lab model use <name>` checks the model server has
  it and asks it to load it. Which model the lab uses is a decision we make, after the bench; the
  command only makes acting on it safe.
- **No sudo to run the lab.** Root installs it and nothing else. The lab's state belongs to its
  own user, `anchorlab`; the installer puts the person who ran it in the `anchorlab` group, which
  can read the state and write one folder, the inbox. Everything a person does to a running lab is
  a file in the inbox that the harness acts on as itself: the list (`targets.json`), the model
  (`model`, read before each test, so switching needs no restart and no edit to root's settings),
  and benches (`bench-<model>.request`, run between tests, so their files are the lab's). Sudo
  for day-to-day work would mean a root shell open on the machine that runs strangers' code, and
  files owned by whoever typed the command.
- **A server is retested per model.** The seven days a test stands are counted per server and
  model, so switching models tests every server again under the new one.
- **A bench with no program in a cell keeps no score.** If the model wrote programs and none
  reached a cell, the cells are broken, and the score would measure the box, not the model.
- **The cells replace the `agent-run` wrapper.** An earlier wrapper started a sandbox by hand for
  each agent, which is what the cell units now do from a file. `install.sh` removes the wrapper when it finds it on the
  box and says so. It leaves the folder of earlier agents' reports alone, and it prints any sudo
  rule that names the wrapper for root to take out.
- **Results reach the site over the same HTTP, as the export.** `GET /v1/results?after=N` on the
  stats service sends the result lines after N, 500 at most, as they are in `results.ndjson`, with
  the line to ask from next. They're already only numbers and fixed words, and the site checks each
  field anyway. The SSH `sync export` answers the same; HTTP is the one in use, because the port,
  its three walls and the site's client were there already for the stats.
- **Testing again is a request in the inbox, like a bench.** `anchor-lab retest` names servers, or
  every one the model failed, or all of them, and a model. The harness runs it between two tests
  as its own user, so it needs no sudo. A slug that isn't on the target list is skipped, so a
  request can't send the lab anywhere the list doesn't. It doesn't switch the lab's model: a second
  model on the first one's failures tells a server's fault from a model's, and a full test with
  another model gives that model's results without changing what the lab runs on.
- **The admin page sees the lab, over HTTP, numbers only.** `anchor-lab stats` (`anchor-lab-stats.service`) answers
  `GET /v1/stats` on the box's own address on the local network, never every interface. It reads
  the state and `/proc` and `/sys` and writes nothing, and runs as a user made for it each start,
  in the anchorlab group so it can read the journal. Three walls, each on its own: the box's
  firewall lets only the site's server reach the port, the unit lets the process talk to this
  machine and private networks only, and the program answers the addresses in
  `ANCHOR_LAB_STATS_ALLOW` and refuses the rest. What goes out follows the results' rule: the run
  in progress, today's counts, the latest twenty results, and the machine (load, memory, disks, the
  GPU's use, memory and temperature). No tool's name or description, no error message, nothing a
  model wrote. The transcripts stay on the box. The site asks at most every ten seconds while its
  admin page is open and checks every field against the format before showing it. SSH through
  `labsync` would have needed no new port, but a login and an ssh process every few seconds for a
  status line. Still a pull: the box holds nothing of ours and sends nothing unasked.

## How it gets to the site

Three steps. The first is built. The other two are designed here and built after a first
real run, because the page should be shaped by what the results look like.

1. **The box tests and writes** (built). `results.ndjson` grows by two lines a server.
2. **The site's server collects** (next). A timer on the site's server asks the box for the lines
   after the last one it has, and a collector that's part of the site's own code checks each line
   with a parser of its own: the version, every field's type and range, the slug against the
   listings, times that aren't in the future, tool names against the pattern. A line that fails is
   dropped and counted. What passes is written to the state directory, one file a listing, and the same timer sends the box a fresh list of targets.
3. **The site shows it** (after that). The files are read into the live state beside the probes, so
   a listing's page and `/api/v1/live/<slug>.json` get a "Lab" panel: when it was last tested, with
   which model, how many tools answered, on which attempt, and how fast. Later the same numbers
   fill Performance and Task success, which changes the methodology and goes in the changelog with
   a version.

## Rejected

- **Letting the program make its own calls, with a firewall around it.** The firewall would have to
  allow the vendor's addresses, which change, and nothing would count or limit the calls. A cell
  with no network is simpler to reason about and gives the measurements for free.
- **`systemd-run` with the sandbox as arguments, or a sudo rule.** Either lets whatever starts the
  cell choose its walls. A unit file installed by root can't be argued with.
- **An import filter on the programs** (no `os/exec`, no `net`). A filter can be got round and
  reads like a wall. The cell is the wall.
- **Publishing what the model says about a tool.** It would be the most readable part and the least
  safe: a tool's description can ask for a good review.
- **The box posting results to the site's API.** It would need a credential on the box, and the
  box is the machine that runs strangers' code.
- **Sharing the result format as Go code between the lab and the site.** The collector should be
  written against the format, not against the lab's source, so a compromised box can't rely on a
  quirk both sides share.
- **Keys for servers that need an account, in the first version.** Each key is something to steal
  and something that can spend. They come later, one listing at a time, injected by the gateway and
  never shown to the model or the program.

## Numbers

| What | Value | Where |
| --- | --- | --- |
| Calls a run may make | 40 | `gateway.DefaultLimits.MaxCalls` |
| One answer's size | 4 MB | `MaxBody` |
| One call's time | 60 s | `CallTimeout` |
| Between two requests to a server | 300 ms | `MinInterval` |
| A call's arguments | 64 KB | `MaxArgs` |
| A tool's answer as the program sees it | 32 KB | `MaxText` |
| Replies asked of the model in one explore | 6 | `MaxTurns` in `cmd/anchor-lab/main.go` |
| Tries at one tool | 3 | `Attempts` |
| A program's build, and its run | 3 min, 90 s (2 min at most) | `buildLimit`, `JobTimeout`, `maxRunLimit` |
| A program, and what's kept of its output | 256 KB, 64 KB | `maxSource`, `maxOutput` in `cell.go` |
| A cell's memory, processes, life | 6 GB, 512, 7 min | `anchor-lab-cell@.service` |
| A server's test stands for | 7 days | `-retest` |
| Between two servers | 20 s | `-pause` |
| Targets from the listings alone, 14 November 2026 | 61 | `anchor-lab targets -data data` |
| Bench cases: callable, traps, servers | 43, 7, 5 | `bench/cases.go`, version `anchor-lab-bench/1` |
| Replies asked of the model on one bench server | 8 | `MaxTurns` in `benchCmd` |
| Passes a bench makes over the set | 3 | `bench.DefaultRuns`, `-runs` by hand |
| The language version a program is built as | Go 1.22 | `go.mod` written by `cell.Serve` |
| One stats reading is served for | 5 s | `-fresh` on `anchor-lab stats` |
| The site asks the box at most every, and waits | 10 s, 3 s | `labFresh`, `labTimeout` in `labstats.go` |
| The Lab tab asks the site every | 15 s, while it's open | `site.js` |
| The stats answer, at most | 256 KB | `labMaxBody` |
| Latest results in the stats | 20 | `stats.Recent`, `labRecent` |

## Where it lives

- `README.md`: building it, installing it on the box, the first run.
- `cmd/anchor-lab`: `run` (the harness), `cell`, `sync` (what the site's server may ask),
  `targets` (run on the site's server).
- `gateway`: the calls, the address check, which tools are called, the limits.
- `cell`: the protocol, the cell's half and the harness's, and `sdk.go.txt`, the package a
  program imports.
- `agent`: the two tasks, the prompt, the arithmetic of a result.
- `journal`: the three kinds of file and the result's format (`anchor-lab-result/1`).
- `bench`: the cases and their right answers (the sample here, a private set by build tag), the
  in-process servers, the score.
- `stats`: what the admin page sees, and the server that answers it.
- `brain`, `targets`.
- `systemd`, `install.sh`: the units and the one script root runs on the box.
- `MODELS.md`, `models/`: the models, their pinned files, the server's presets, and the scripts that
  fetch them and set the server up.
- `private/`: ignored by git; our results, settings and notes.
- `build.sh`: vet, test and build, with a private bench set or without.

## What was run, and what wasn't

- Run: the tests, and `systemd-analyze verify` on the units. One test is a
  whole run: a fake MCP server, a scripted model, and real programs compiled and run by the cell's
  code over the real protocol, with a tool that says it deletes and carries an instruction in its
  description. The cells in the tests are plain processes, without the unit's walls.
- Run end to end through the real command: `anchor-lab bench` against a stand-in model that got one tool wrong on purpose and tried one trap. It scored 42 of 43 with that
  tool and that trap named. The first try failed every build, because the cell's `go.mod` asked
  for Go 1.26 and that machine had 1.24, which is why it now asks for 1.22.
- Run against real servers: `connect` only, once each, against Exa's and ReqRes's hosted MCP
  servers. Both opened and listed their tools (2 and 12). Of ReqRes's 12, 8 are named like a change
  (`create_project`, `patch_record` and so on) and were marked not to call.
- Not run: the units on a real machine, and `explore` with a real model against a real server. The
  first run on the box is the test of both. The likeliest thing to need a change is the cell
  unit's system call filter, which fails closed: a cell that can't build says so in the journal.

## Open

- **HTTP APIs.** Over half the listings on the site are plain APIs, not MCP servers. They need a second kind of call in
  the gateway (same address check, same limits) driven by the listing's OpenAPI file.
- **Tasks with a right answer.** `explore` measures whether a tool can be called. Task success
  needs tasks a category shares and an answer the harness can check without a model's opinion.
- **Servers that run on the caller's machine** (stdio). Running a vendor's package is
  a different risk from calling a vendor's server, and needs a cell of its own kind, with a
  network. Not before the rest has run for a while.
- **Open-weight models**, tested as models on the box itself.
- **Keys**, as above.
- **A second wall on the network.** The box's own firewall keeps it off the local network. A block
  on the router or the switch that doesn't depend on the box is still to do.
- **How long transcripts are kept** on the box, and whether a sample is read by a person or a
  frontier model each week.
