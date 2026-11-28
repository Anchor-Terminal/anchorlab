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
  model or the program: the only key the gateway sends is one we were given for that server,
  which the box opens itself (below).
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
- **A change tool is called only in a dry run it declares.** Tools that create, send or delete
  aren't called, because calling them on a production server would publish reviews, send mail or
  remove things (it would on Anchor Terminal's own server). That leaves a server's write tools
  untested, and a server can only be shown to work if all its tools are tried. Vlad, 18 November
  2026: "we should have on our chnage of state mcp commands a flag that would return what we would
  have done if the flag was not test". MCP has no dry run, and the servers that have one each name
  it differently (`dry_run`, `__confirm`, a separate preview tool), so the lab reads one
  declaration: `"_meta": {"anchorterminal.com/dryRun": true}` on the tool, with a boolean `dryRun`
  in its input schema. Such a tool is callable, destructive or not, and the gateway sets
  `dryRun: true` on every call to it itself, whatever the program sent, so a model's program can't
  make it real. Both halves are needed: a `dryRun` argument alone could be one the server ignores,
  and a declaration alone has nothing to set. A result marks the tools called this way.
- **The numbers are the gateway's, never the model's.** Calls, errors, latency and which tool
  answered on which attempt are counted where the calls are made. The outcome is arithmetic over
  those (every callable tool answered without an error at least once: success; some: partial;
  none: failure). The model is asked to try, not to judge.
- **Two tasks to start.** `connect` opens the server and lists its tools, with no model. `explore`
  asks the question an agent has on its first day: given only what the server says about its
  tools, can I call them? It measures something the site can't read off a page: how many tries a
  model needs to get each tool to answer, and how fast the server is when it does.
- **A plain HTTP API is tested the way an MCP server is.** Asked what would make the harness best
  at testing tools, Vlad, 22 November 2026: "focus on HTTP APIs i don;t think MCP is where the
  future is, it is just writing inegrations for difreent apis to get what we need out of them".
  Over half the listings are APIs with no MCP server, and the lab tested none of them. What the
  model is given for an MCP server is what the server says about its tools. For an API that's its
  OpenAPI document, so the gateway reads it and makes each operation a tool: its name, its
  summary with the method and the path in front, and its path and query parameters as the
  arguments (`gateway.OpenAPI`). A call is one request built and sent by the gateway, so
  everything else is as it was: the cell has no network, a program's only way out is `tool.Call`,
  the result is counted from the calls and not from what a model says, and the journal, the
  per-tool counts and the two results a run makes are unchanged. Letting the program make its own
  HTTP requests was the other way, and it was turned down for the reason it always is here: nothing
  would count or limit them. The rule that a tool which changes something is never called becomes
  the method. Only GET is sent, and every other operation is listed as never sent, the way a
  write tool is. A request the lab can't build (a parameter missing, or one the operation
  doesn't take) counts as a try that failed and tells the program why, as a server's own
  validation would. A 4xx goes back with the API's words, because whether a model can fix its
  request from them is part of what's being tested. A run offers 60 GET operations at most, since
  a run's calls can't cover more and a big API has hundreds. The target list gains `openapi`, the
  address of the description, and a list without it means what it always meant.
  `anchor-lab api` tries one API by hand and keeps nothing. Tested against made-up APIs, with real
  compiled programs, and not yet against a real one.
- **The harness reads YAML itself.** Vlad, 22 November 2026: "let's add a YAML reader to the
  harness, we should be able to read that". An API's description is JSON or YAML, Go's standard
  library reads only the first, and no dependency gets added here, so the lab has a reader of its
  own (`yaml/`). It turns YAML into the values `encoding/json` would have made of the same
  document, and `gateway.OpenAPI` carries on with it as JSON, so there's one path after the
  reading and not two. What it reads is what the OpenAPI specification asks a description in YAML
  to be: YAML 1.2, JSON's kinds of value, keys that are strings. That's block and flow mappings
  and lists, plain and quoted scalars, literal and folded blocks, comments, anchors, aliases and
  merge keys. By 1.2's rules `yes` and `no` are words, and a number keeps the text it was written
  with, so `swagger: 2.0` is still 2.0. It refuses a complex key (a line that starts with `?`),
  skips a tag other than `!!str`, and reads the first document in a file. It's a reader and not a
  validator: a description with a stray comma or a comment in the wrong place still reads.
  The input is a stranger's, so it's bounded. A document can't come to more than 2,000,000
  values, the copies its aliases make included, so a small file whose aliases multiply is refused
  and not expanded. It can't nest past 120 levels. Every line is read once however long a quote or
  a bracket stays open. Nothing in a document makes it panic, which a test over random pieces of
  YAML and a fuzz target hold it to. The description gets a size limit of its own, 32 MB
  (`gateway.MaxSpec`), where it had an answer's 4 MB: it's read once a run, a model never sees it
  whole, and GitHub's is 11 MB.
  Checked on 22 November 2026 against three things. Stripe's description (6.6 MB) and GitHub's
  (11 MB), which are each published in YAML and in JSON: both read as the same document as their
  JSON, apart from 4 example values in GitHub's (`0.16001e0`) that are a number by YAML 1.2 and
  text by 1.1. The APIs.guru directory (commit f04b8d0, 4,138 descriptions, 750 MB): all 4,138
  read, and 4,103 are the same document PyYAML's libyaml reader makes of them with the same rules
  for plain scalars. The other 35 PyYAML refuses (32 for a tab at the start of a block, 2 for a
  control character, 1 for a U+2028, which ends a line in YAML 1.1 and not in 1.2), so there was
  nothing to compare them with. And YAML's own test suite (`yaml/yaml-test-suite`, 402 cases): of
  the 279 valid ones it gives the JSON for, 260 read as that JSON, 19 are refused (13 of them for
  a complex key) and none is read as something else. Of its 94 cases that aren't YAML, 51 are
  refused and 43 read. `ANCHOR_LAB_YAML_DIR` and `ANCHOR_LAB_YAML_SUITE` run those checks again
  (README). PyYAML was only the check, and isn't in the repository or on the box.
  Through the gateway, 3,293 of the 4,103 open as the same API from YAML as from JSON, with the
  same address, operations and arguments, and 810 are refused the same way: 395 have no GET
  operation, 390 have a server address with variables in it, 14 aren't OpenAPI documents with
  paths, 10 give an address the lab won't call, and 1 is over 32 MB (Microsoft Graph's beta, at
  58 MB). The target list's `openapi` still means the address of the description, and what's at
  it may now be YAML. What none of this shows is whether a model can call an API from its
  description. No real API has been called yet.
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
  recommends (about 1.0) and is low enough to make a thinking model loop. Thinking is capped
  (2,048 tokens of a 4,096-token reply then, 8,192 of 16,384 since 22 November 2026) so a model
  that thinks still has room in the reply for the program.
- **Thirty models, for the evaluation to rank.** Vlad, 18 November 2026: "We should run probably
  around 30 models". The eight the calculator picked are seven now: Qwen3.6-27B went, since
  Qwen3.8-27B replaced it at the same size, and Qwen3.8-27B runs at Q8_0, which fits with room to
  spare. The other 23 are every open-weights listing whose GGUF fits (a file plus a fifth within 96
  GB) and that's built for agents or tool calling, scored or not: the calculator can only rank the
  ones a leaderboard has read, and our own evaluation is how the rest get a number. About 1.02 TB on
  the box's 4 TB.
- **A third party's 3-bit build where the publisher's won't fit.** Qwen3.8-Flash-Next scores 74
  against the 122B's 62, and its smallest publisher GGUF is 103 GB. ISTA-DASLab's GSQ-RCO IQ3_XXS
  is 75.8 GB and, by their card, matches the full model on AIME25. It's below the 4-bit line we
  record elsewhere, which is why it's named as theirs; the evaluation says whether it holds up.
- **Reading the logs needs no sudo.** Both installers put the user who ran them with sudo in
  `systemd-journal` as well as `anchorlab`, so `journalctl -u llama-server`, `-u anchor-lab` and
  `-u 'anchor-lab-cell@*'`, which the docs and the harness's own errors point to, work as that user.
  It's Debian's own group for reading the system journal, and it only reads. It shows every unit's
  log on the box, not only the lab's, to a person who already installed with sudo. A log file per
  service readable by the anchorlab group would be narrower, and would mean rotating files
  ourselves. Found on 18 November 2026, when the hint models/install.sh prints showed nothing.
- **Variants: the same model, one setting changed, on the same questions.** Vlad, 18 November 2026,
  on speculative decoding: "can we add benchmarks for speculative and non speculative and on
  different temperatuires? same model, different settings, same evals", and "can we add the 4b as
  well as the 8?". A variant is a preset named `<model>+<what changed>` (`qwen3.8-27b+q4`,
  `+mtp`, `+t0.6`), so llama-server serves it like a model and the evaluation, the bench and the
  queue need nothing new; `+` is a character every name check already takes. It was `:` for an
  hour on 18 November 2026, until the variants showed up in the admin page as `qwen3.8-27b:6`,
  `:DFLASH`, `:MTP` and `:Q4`: llama-server reads a preset name's part after a colon as a quant
  tag, keeps its last `-` or `.` piece and capitalises it (`canonical_tag` in common/preset.cpp),
  so seven variants came out as four names. `+` means nothing to it. models.txt lists every file a variant loads, the shared ones too, so its size is what it
  needs; `fetch.sh remove` keeps a file another model names, so removing a variant never takes its
  model's weights. The first set is qwen3.8-27b's: the 4-bit build, Qwen's MTP heads and the DFlash
  draft from the same ggml-org repository on each build, and temperature 0.6, because a dense model
  that reads all 28.6 GB for each token is where a draft pays most and where the 7.6 tokens a
  second we measured hurt most. Each variant keeps the publisher's sampling unless its name says
  otherwise, so a difference in the score is the thing named. The Lab score stays the plain
  model's, at the publisher's settings; a variant's scores are the comparison beside it.
- **A harness adapter is a variant, and the question is whether it travels.** Vlad, 22 November
  2026, asked for harness-hub's OpenCode adapter for Qwen3.8-27B to be added to the list. It's a
  LoRA of rank 32 trained with reinforcement learning inside the OpenCode coding agent, and its
  card's results are all under that harness. Ours is a different one, so what the lab can add is
  the thing the card can't say: whether an adapter trained for one harness helps, does nothing or
  hurts in another. For that the comparison has to change one thing, so the adapter comes with a
  partner. `qwen3.8-27b+nothink-t0.8` is the plain model with thinking off at temperature 0.8 and
  top_p 1.0, which is how the card ran both sides, and `qwen3.8-27b+opencode` is the same with the
  adapter loaded (`lora` in its preset). The plain `qwen3.8-27b` stays the Lab score, at Qwen's
  settings. The card says the GGUF build of the adapter hasn't been evaluated again, so a poor
  score here could be the conversion and not the adapter, and the notes say so. Neither has run.
- **Removing and swapping a model needs no sudo.** "A command to remove a model and switch to a new
  one" (Vlad). `models/fetch.sh remove`, `swap` and `prune` work on the models folder as the user who
  downloads, and switching is `anchor-lab model use`, a file in the inbox, as before. `remove`
  refuses the model the lab uses; `swap` downloads and checks the new one before it switches or
  deletes anything. Adding a model still needs its preset installed, `models/install.sh`, which is
  installing.
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
  the box already has Go for the cells. That puts updating the lab on the box (`./deploy.sh`, below)
  with nothing copied from the site's server but the private bench set,
  one file in `private/`. Earlier the box only ever got a binary built elsewhere; that was about
  keeping our private repository and its keys off the box, which this still does.
- **One command deploys, and the lab carries on.** Vlad, 22 November 2026: "also how do i install
  the agent lab, can we have a deploy that does evrything?", and "the pull, the install and
  everything else and just continues". An update was five steps by hand (pull, build, two installs
  as root, a new list), easy to do in the wrong order or half. `./deploy.sh` does them all, as the
  ordinary user from the clone, and asks sudo only for the two installs, so the rule that root
  installs and nothing else holds. It asks for the password once at the start and keeps sudo fresh
  in the background, so nothing waits on an answer halfway. The order is what makes it safe to run
  any time. The pull and the build, with its vet and short tests, come before anything is touched,
  so a bad commit leaves the running lab as it was. Then the lab is paused through its inbox, the
  way a person pauses it, so what's in progress stops where it goes on from before anything
  restarts. A llama-server restart under an evaluation would have cost the question in flight. A
  pause someone set themselves is left alone. The installs follow, and the pause is lifted however
  the deploy ends. "Just continues" needed three changes to the harness and the model installer.
  A harness started under a pause holds before it starts anything (`pausable` checks the inbox
  before it returns), where it used to start a test and stop it a moment later. A retest stopped by
  a restart keeps the servers it hadn't reached, and the one it was on, in the inbox, as it already
  did for a pause. And `models/install.sh` restarts llama-server only when its presets, its unit or
  llama.cpp changed, since the deploy runs it every time and a restart unloads the model. An
  evaluation already went on from its checkpoint and a bench from its start. A model that won't
  download, or a list the site doesn't give, is said and passed over, since the rest doesn't need
  it. A `deploy.sh` changed by the pull hands over to the new one. llama.cpp is rebuilt only with
  `-llama`: a new llama.cpp can change what a model does, so updating it is a decision and not a
  side effect. Tested as an ordinary user with a stand-in for sudo, systemctl and the harness, not
  yet on the box. The first `-llama` on the box, on 24 November 2026 for Inkling 2 Mini, stopped
  at "LLAMA_CPP in private/settings.env names no llama.cpp checkout": the setting was new and the
  box's settings didn't have it. So an empty `LLAMA_CPP` is looked for (a clone of llama.cpp up to
  three folders down from home, beside the anchorlab clone, or in `/opt`, `/srv`,
  `/usr/local/src`), and with none a new one is cloned to `~/llama.cpp`; either way its path is
  written to `private/settings.env`, which `models/install.sh` reads as root. Two checkouts, or one
  made with sudo that the user can't pull into, stop it with what to do.
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
  holds no key, token or private checkout of ours that gets it anywhere: its own key pair opens the
  keys sealed to it, and its command secret only lets the site in. The site asks for results and
  stats over HTTP and sends signed commands; the SSH `sync` user from the first design still
  answers the same export but isn't needed.
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
- **An evaluation asks real questions with one right answer, of our own server.** The bench
  measures whether a model can get a tool to answer, on servers we made up; explore measures a real
  server. Neither says whether the model got a question right. Vlad, 18 November 2026: "Can we set
  up an evaluation using our own mcp as the test to see which one does better, this way we can test
  against something real". Anchor Terminal's own MCP server is the one to ask: the data is ours, so
  we know the answers; we can call it as often as we like; and every write tool on it takes a dry
  run. So `anchor-lab eval` puts 59 questions about the site to a model, which answers by writing
  programs, as in explore.
- **The tasks mix the MCP server, pages and the API, and some are hard.** "We should make it kind
  of complicated at times otheriwse they will all get 100, like a mix of mcp and reading a page and
  api" (Vlad). Beside the MCP server's tools a program has one tool of the lab's, `lab.fetch`, a
  GET on the site and nowhere else (`eval.SitePath`; the address check is the gateway's), up to 4
  MB, so a program can decode the API's files itself. Level 3 tasks filter, join files and do
  arithmetic over hundreds of listings. DNS (the site's grade records) was left out: it didn't
  resolve from where the set was written, and a task that fails for the network is a task that
  measures the network.
- **Right answers are worked out from the site when the task runs, and the set is public.** A
  written-down answer goes stale the day a listing is re-graded. Each task's answer comes from the
  public API as the task starts, so the set stays right as the data moves, and a model can't learn
  answers that change. Eight have answers that don't move, as a floor: "known things we're small
  enough that nobody would care about training on us" (Vlad). The private-set rule the bench keeps
  isn't needed here, for those two reasons.
- **Stale data never costs a model a task.** "Make sure we don't fall for stale data and fail
  tests because our tests are not robust enough" (Vlad). An answer is checked against the site as it
  was when the task started, and if that doesn't match, as it is when the model answered. A task
  whose subject left the site, or that has two right answers right now (a tie), is skipped for the
  pass, not failed; one where the site didn't answer the harness isn't counted, and a run where that
  was most tasks keeps no score. `anchor-lab eval -check` works every answer out with no model, to
  find a task the data broke before an evaluation runs. The harness reads the site with
  `Cache-Control: no-cache`, and the MCP server and the API are served from the same loaded data, so
  the two a model reads from agree.
- **An answer with no lookup behind it is wrong.** The questions are about the site today. A model
  that answers from memory gets nothing for it, even when it's right, except on the three tasks
  the tool list it's shown answers by itself.
- **A tool's answer cut short is our tool to fix.** The gateway hands a program at most 32 KB of a
  tool's answer. "We should inpove our tools if the output got truncated" (Vlad). The gateway marks
  a call whose answer was cut, and a score lists our tools by how often, so the evaluation finds
  answers too long for an agent as well as models that can't cope.
- **One number, 0 to 100: the Lab score.** "We need to give them a number 1 to 100, so another
  benchmark, we can then feed that back into the model listing" (Vlad). It's the tasks right with a
  level 2 task counting twice and a level 3 task three times, out of the tasks scored, weighted the
  same way, so two models that both get every easy question right are told apart by the hard ones.
  Scores are ranked by it, and it's what the site shows on a model's listing.
- **A model the box can't run gets the same grading.** The scores on the site were all from the
  box, so nothing said how hard the set is for a model that isn't. Vlad, 21 November 2026, of the
  first eleven Lab scores: "did we make it too easy? or it just normal usage? how would you do on
  it?", then "yeah, run it". `anchor-lab eval -questions` prints what a model in the harness is
  told, `-snapshot <file>` writes the right answers to a file and prints none of them, and
  `-grade <answers> -before <snapshot>` grades answers made elsewhere with `eval.GradeGiven`, which
  ends in the same `grade` a task on the box does. What was called comes from a log of the calls,
  never from the model's account of itself. Several files are passes of one model and fold as an
  evaluation's passes do. The set's author can't take it honestly, so the rule for a run by hand
  is Vlad's: "make sure the context is all gone when they run the tests, same for you, new
  instance running with no history". Each pass is a new instance given the brief and a logged
  runner and nothing else. What this can't copy is the harness: its turn limit, its cells, a new
  conversation for every question. So such a score is printed with "graded outside the harness",
  is never written to `eval.ndjson`, and is never ranked beside the box's.
- **The hard set is kept beside the normal one, and isn't the Lab score.** Three hosted models
  put through the normal set that way all scored 100 (below, "What was run"), and the best local
  model scores 98, so the set is near its ceiling. Vlad: "can we have a hard one as well, we'll
  keep it for the new versions if it gets saturated for now we still run the normal one", and "i
  want somethinh hard enough for haiku to not necessary get 100%". `eval/hard.go` is 66 questions
  under their own version, `anchor-lab-eval-hard/1`, run with `-set hard`. The normal set stays
  the default, the one the admin page asks for and the only one ranked, because eleven models
  already have a Lab score on it and a score on a different set can't sit in the same column. So
  the hard set touches nothing the Lab score reads: its scores are in `eval-hard.ndjson`
  (`eval.Load` reads only the normal set's file), `/v1/evals` doesn't serve them, its checkpoints
  are in `eval/resume/hard/` so a model stopped half way through the normal set keeps that
  checkpoint, and its request in the inbox is `eval-hard-<model>.request` with `hard` on its third
  line (`journal.EvalSet`). In the stats it's an evaluation like any other, with no new word, so
  the site's side needs no change until it wants to show hard scores. When the normal set is
  retired, that's a decision of its own and a change on the site in the same pull request.
- **What makes a question hard is steps, a plan and a deliverable, not obscurity.** Every hard
  question still has its answer worked out from the public API when it runs, and its wording says
  which rows count (ranked or all, at or above a grade, ties broken how, and a listing's category
  as the rankings file gives it, because the categories file also lists a listing under the
  categories it's "also in"). The set was written three times against Haiku, on Vlad's test for
  it. The first 44 questions were joins, arithmetic with the benchmark's weights, a page to read
  past the 12,000 characters a direct call returns, and near-miss traps (a slug one word off, a
  real model under the wrong provider, a category one letter short, a count that's 0 and not
  NONE). Haiku got all 44 in three passes out of three. What a model that good gets wrong isn't
  the arithmetic, so the next thirteen and the three after them went after the plan: questions
  that need a listing's own JSON for more listings than the 40 fetches a question gets (50, 60,
  61, 80, 177). The model isn't told the limit. It finds out when a fetch is refused, and the way
  through is to see that `search_tools` filters on the same field and says how many match, or
  that `get_tool` reads a listing on a count of its own. That was the first thing to cost Haiku a
  question. Then Vlad: "maybe more open ended questions, that need a lot more work, at least a
  few". Six work tasks have something to hand in: a stack of four listings that keeps four rules
  and comes within three points of the best total possible, two ordered lists, an audit of where
  two of our files disagree, a report of four numbers, and a count by company over 45 listings.
  The stack has many right answers, so a task can carry a verifier (`Task.Verify`) that says
  whether this answer is one of them, and its `Want` gives one as an example or says the rules
  can no longer be met. An answer can also be a list in order, a list in any order or several
  numbers (`InOrder`, `AnyOrder`, `Numbers` in `eval/check.go`), compared part by part. A question
  two rows would tie on, or a rounding could decide, has no answer for that pass (`ErrAmbiguous`),
  as in the normal set. Every task is tested on a made-up site whose answers are known by
  construction (`eval/hard_test.go`), so the tasks' own sums were checked without reading the
  real answers.
- **A run by hand keeps the lab's limits.** The first runs by hand had none: a fetch of any size,
  any number of calls. The runner used for them now refuses the 41st fetch and the 41st tool call
  of a question, a fetch over 4 MB and more than 32 KB of a tool's answer, as the gateway does
  (`gateway.DefaultLimits`, `eval.MaxFetch`), because a question about planning inside a limit
  means nothing without the limit. It still isn't the harness: one conversation runs the whole
  pass, so what a model learns about our data on the first question it keeps for the rest, where
  on the box every question starts cold with 16 turns.
- **Being told it isn't there is a lookup, for the hard set's traps.** "An answer with no lookup
  behind it is wrong" counts only lookups that answered: a fetch that came back 200, a tool call
  that wasn't an error. For a task whose right answer is NONE that's backwards. `get_tool` answers
  a slug that doesn't exist with a tool error, and `/api/v1/tools/<slug>.json` with a 404, and a
  model that asks exactly that and answers NONE has done the task and is marked wrong unless it
  also made some other call that worked. The runs by hand showed it: Haiku and Sonnet each lost
  NONE questions in a pass where their only lookup was the 404. So a hard task can say
  `NotFoundCounts`, and its three NONE traps do: a 404 or a 410 from the site, or a tool that
  answered with an error, is the lookup (`TaskResult.Asked`). The normal set's four NONE traps
  had the same flaw. They couldn't be changed in place: how an answer is checked is part of a
  set's version, the eleven Lab scores were made under `anchor-lab-eval/1`, and changing the rule
  under them would make new scores and old ones differ for a reason nobody can see. The next
  decision is what was done about it.
- **The normal set's traps are fixed in a second version, and the saved runs are graded again,
  not run again.** A new version usually means every model runs again, which was about 67 hours
  of the box for eleven models. Vlad, 22 November 2026, chose to grade the saved runs again
  instead. It's sound here because `anchor-lab-eval/2` asks the same 59 questions and changes
  one check, and a run's journal already holds what that check needs: each question's
  `answer.txt`, and how every call and fetch ended. So `eval.Rescore` reads a score's own run
  and looks at the four NONE traps only. A pass changes when it was marked wrong, no lookup
  answered, at least one was answered with "it isn't there" (a 404 or a 410, a tool's error) and
  the answer was NONE. It can only go from wrong to right. The run's time, tokens and every other
  task stand as they were, and the new score says where it came from (`from` on the score, the
  version it was first graded under). No model is asked and the site isn't read. Where the
  journal and the saved score count a trap's passes differently, or a pass isn't in the journal,
  that task is left as it was, which can only keep a score lower than it should be. A score
  whose run isn't in the journal at all isn't carried: that model has no score of version 2
  until it runs again. The harness does this itself when it starts (`carryScores`), because a
  box that serves version 2 with no scores in it would empty the site's page until somebody
  remembered a command, and it writes one event a model with the score before and after
  (`eval.rescored`). `anchor-lab eval -rescore` prints the same thing and keeps nothing, so the
  change can be read before the harness that makes it is installed. Both versions' scores stay
  in `eval.ndjson`: version 1's are the record of what was published under it.
- **A third version: the box's failures aren't the model's, and a trap needs a look at the thing
  asked.** A review of the harness at `fd6f6d6` on 23 November 2026, by another model with
  fixtures of its own, found seven faults, and each was true of the code. Four were in how an
  answer is checked, so they're `anchor-lab-eval/3` (and `anchor-lab-eval-hard/2`), graded again
  from the journal like version 2 was (`eval.Rescore3`, `eval.CarryAll`):
  - *A model's server that failed was the model's miss.* A request that failed ended the
    question with no answer, which scores as wrong. Now the model's client says which failures
    are its server's (`brain.Down`: no connection, a dropped one, a reply past the harness's own
    timeout, 5xx, 404 for a model it doesn't have, 401, 403, 429, or a 200 that isn't a chat
    completion) and which are the conversation's (400 and 422, after the harness has already
    retried with less of it). gBench draws the same line for its agentic suite's requests, and
    doesn't score its harness's errors either. A question a server or a cell failed under is
    asked again once after 20 seconds, then is `failed`: left out of the score, counted in the
    score's `failed`, and not written into the checkpoint, so a stop asks it again. More than one
    question in fifty failed (gBench's line for failed requests is 2% too) and no score is kept,
    since it would be over other questions than other models' scores, and the next run asks only
    the failed ones. A model that got every reply and gave no answer is still no-answer, and a
    model that called a ruled-out tool before the failure is still wrong. What the model's own
    program did to the cell (a program over 256 KB, a tool name or a call too long for it,
    `cell.ErrProgram`) isn't the box failing: the model is told, like a build error, and goes on.
    An outage doesn't send either mode to its tool-free fallback, which is for a server that
    refuses tools, so the question isn't finished in a conversation the outage changed. The event
    for a failed question is `question.failed`; `eval.failed` stays the whole evaluation failing.
  - *Any 404 justified a NONE.* Version 2 let a 404 count as the lookup on a NONE trap, and the
    review fetched another listing, got its 404, answered NONE and was right. Now each trap names
    what it's about (`Task.About`, words like `zorblax` or `gpt-7`) and the files that list every
    candidate (`Task.Covers`, `rankings.json`, `prices.json`, `categories.json`), and only a lookup
    whose path or arguments hold one of the words, and that was answered, or a read of a covering
    file (by `lab.fetch`, or a tool such as `read_page` asked for its path), counts. The words are
    the thing asked about and not what's near it: `stripe-api` and not `stripe`, whose listing is
    real and is the trap; `anthropic-api` and not the model's id, which is listed under another
    provider. A provider's error for the right listing doesn't, since it says nothing about
    whether the listing is there. To tell, the gateway keeps each call's arguments in memory
    (`gateway.Call.Args`), never in an event or a result.
  - *A refused call to a ruled-out tool vanished.* The tally skipped calls the gateway refused
    before it checked the task's ruled-out names, so a model that tried `submit_review` against
    "don't call it" was right if the gateway stopped it. The model is what's graded and the
    gateway refusing is the lab protecting the server, so an attempt counts, sent or not
    (`TaskResult.Tried` counts the refused ones).
  - *"1/2" was 12.* The number reader took out everything that wasn't a digit. Now an amount is
    one number with a currency, a percent sign, commas between thousands or a unit word around
    it, and two numbers, or two joined by a slash, an operator or a dash, isn't one. Nor is a
    number written onto a name (`gpt-4`, `top-3`, `v2`) or with a scale or a fraction after it
    (`16k`, `1.5 million`, `12½`), and a typographic minus is a minus.

  Grading the saved runs again works from what the journal has. A no-answer pass in a run with a
  `model.failed` or `cell.failed` event is failed (an old `model.failed` didn't say whose it was,
  and is taken as the server's, since a refused request is retried with less of the conversation
  first, unless that run's conversation had already been cut to fit the context, when the refusal
  is likeliest the conversation outgrowing it and stays the model's). A score that would leave
  out more than one question in fifty that way isn't carried, as a run now wouldn't be kept. A right pass with a call event for a ruled-out tool, of any status, is wrong. The
  events don't keep paths or arguments, so a right pass of a trap stands only if the run's own
  programs (`code-*.go`) or its native calls (the `[call ...]` lines of `model-*.txt`) name the
  thing or a covering file. Not a reply's prose, which repeats the slug it was asked about whether
  or not it looked. This can pass a program that named it on a line that never ran, never the
  other way. A right number
  stands only if `answer.txt` reads as one number, the same one. From now on `model.failed`
  says `down`, and `eval.end` carries `about` and `tried`, so a later version can be graded again
  from the events alone.
- **The other three faults are the harness's, and change no set.** A plain API's answer that
  broke off before its declared length was `ok`, and the half body went to the program. It's now
  `incomplete`, with its status and byte count kept, and an MCP answer that broke off before its
  JSON-RPC message was whole is too; the status words are the gateway's, never the result format's,
  which counts anything not `ok` as an error. A checkpoint cut short in the middle of a line had
  the next line written onto the end of it, and both were lost on the next start, so the question
  after a stop was asked twice. Every file of lines the lab appends to (the checkpoint, the
  events, `results.ndjson`, `eval.ndjson`, `bench.ndjson`) now ends a cut-short last line first
  (`journal.MendTail`), keeping it as a line of its own so a reader that counts lines keeps its
  place, and a checkpoint that can't be written says so. A folded bench score added its passes'
  wall time where its own comment says it's one pass's, so a three-pass score looked three times
  slower than a one-pass one. It's one pass's now, with the total in `allMs`, and a score folded
  before is put right when it's read.
- **A vendor's explore the box failed under isn't written.** The same outage counted against a
  vendor's server: a model's server that fell over in the middle of an explore ended the run,
  and the calls so far were written as the server's `failure` or `partial`. Now an explore the
  model's server or a cell failed under before every tool had answered isn't written at all
  (`explore.withheld` in the events, `Result.Withheld`, which isn't part of the format), and the
  server is tested again on the next round rather than after the retest interval. One that got
  every tool to answer before the failure is written, since it's a success either way. A retest
  asked for through the inbox that's withheld goes back in the inbox for that model.
- **What was taken from gBench.** Vlad, 23 November 2026, beside the review: "have a look at
  https://github.com/google-deepmind/gbench ... try to take as many of the good things from the
  google deepmind one". gBench (Google DeepMind's benchmark suite for open models, read at
  `d3e4283`) is built around numbers that can be trusted and compared, and these of its habits fit
  a harness this size:
  - *A harness error is never scored* (the decision above), and an empty score isn't a 0: an
    evaluation where nothing could be scored keeps no score, as gBench withholds one.
  - *Every reply is classified.* The model's client reads `finish_reason`, and a reply that
    stopped at the token limit is counted (`truncated` on the question and the score, `model.cut`
    in the events). `anchor-lab why` gives "cut off at the token limit" as the reason a question
    was lost when it was, rather than "no answer", which read as a model that gave up.
  - *The timeout follows the budget.* A reply could be 16,384 tokens and the dense models write
    about 7.6 a second, which is 36 minutes, against a fixed 15-minute timeout that cut the longest
    replies off and scored them as no answer. A reply now gets `-model-timeout` or its whole
    budget at 5 tokens a second plus two minutes, whichever is longer, and a reply cut off by it is
    the harness's (`brain.Down`). gBench scales its server's timeout to the model the same way.
  - *How a model was asked is written on its score* (`setup`: mode, replies, calls, fetches, the
    reply budget, temperature, context, a program's time limit), since the limits have gone up
    since the first scores. `eval -compare` says when the scores it lists weren't asked the same
    way. gBench hashes this into an id and compares only within one; here it's a line to read,
    and the site still ranks every latest score.
  - *The harness says which code it is.* The version is when it was built and its commit, with
    `.dirty` for uncommitted changes (`2026.11.24-0130+fd6f6d6cb4ee`), where it was a time only.
  - *A change to the checking is a decision.* A test hashes the functions that decide a grade
    (`eval/scoring_test.go`) and fails when they change, until someone says whether a grade can
    move (a new version of the set) or can't (a new fingerprint). Beside it, every kind of answer
    is tested both ways, the forms that count and near misses that mustn't, which is how gBench
    writes its Golden Set: a presence check always says what has to be absent.
  - *Uncertainty is said where models are compared.* `eval -compare` gives each share right a 95%
    Wilson range, and `eval -flips` a two-sided sign test on the questions two models disagree on,
    so "98 against 96" can be read as luck or not. gBench publishes no interval on accuracy; on 59
    questions one is needed.
  - *Smaller ones.* "unknown" isn't a NONE answer any more (a model that doesn't know hasn't said the
    thing isn't there; graded again like the rest). A vendor API's 429 is the API being busy, not
    the program's argument error to fix: it's `http_error` now, where it was `tool_error`, and
    both count as an error in the result format, so `errors` and `toolsOk` read the same and only
    the gateway's word changed. Every file of lines is synced and mended before an append.

  Then, the same night, "yeah do all of them" of the rest, and of gBench's three tiers, "Golden
  Set smoke tets should be good and 2 agentic quality suite is what we are tetsing for! so they
  should be good to port":
  - *Readiness questions before a model is scored* (package `golden`, `anchor-lab-golden/1`). Seven
    questions with one right answer each, in gBench's Golden Set's design and written for the lab:
    a tool call, a tool call with arguments, exact JSON, a conversation of several turns and a
    tool's answer read back. The last two come in pairs: the same thing asked in one plain user
    message (the twin), which a template can't get wrong without everything failing. Twin right
    and pair wrong is the chat template broken, not the model weak. A reply holding the
    template's own tokens (read out of `/props`' `chat_template`, so it covers any family) fails
    too. A broken template holds the run back, after asking a second time (at the publishers'
    temperatures one wrong answer can be chance), since the score would measure the template; a
    weak model is scored like any other, and what it got wrong is written on the score (`ready`).
  - *A preflight before a run* (`cmd/anchor-lab/preflight.go`): disk free, the state writable, the
    site and its MCP server answering, a cell building and running a program, the model loading,
    its sampling and the readiness questions, in that order, stopping at the first that fails.
    An evaluation and a bench run it; `anchor-lab preflight [-model x]` prints every line.
  - *The sampling the server really uses is checked against the preset.* `/props` gives the
    sampling llama.cpp uses when a request sets none; it's compared with `models/presets.ini`
    (leaving out the temperature when `-temperature` is given), and written on the score with the
    server's build and the first 12 characters of the model file's SHA-256 (`server`). A
    difference is warned about and written down, not stopped on: a preset changed on purpose is
    the operator's call.
  - *A stopped evaluation carries on only when it's asked the same way.* The checkpoint keeps the
    setup, and one stopped under other limits starts over ("asked differently"), so a score is
    never passes asked two ways.
  - *Reply health.* Each reply is classified apart from its grade: empty, looped (a line of 40
    characters or more five times, or the last 200 characters four times, in a reply of 800 or
    more), or the answer left in the thinking (an ANSWER line in `reasoning_content` and none in
    the reply). They're counted on the question and the score (`health`), and `anchor-lab why`
    gives them as the reason a question was lost.
  - *What each pass alone would have scored* (`passPoints`) and the share right over every pass
    (`avgPct`), since the majority over passes hides how much one pass's luck moves a score.
  - *A score that measured the limit says so.* More than one reply in ten cut off at the token
    limit puts `replies-cut` in `measure`.

  None of this changes a grade (the fingerprint moved, for the score's new fields, with the set's
  version kept). Still to do: not publishing a P95 from fewer than 20 calls, which changes what
  `p95Ms` means in the result format and so is a new version with the site's side in the same
  pull request; and the agentic quality suite, which is a set of its own.
- **What the lab is doing shows where Vlad looks.** Vlad, 18 November 2026, of the admin page's Lab
  tab: "i should see what it's doing on the lab page, that's where i wanted it". The stats carry
  the evaluation in progress (which question of which pass, how many right, wrong and skipped so
  far, when it started) and the queue in the inbox, in the order the harness takes it, and
  `anchor-lab status` prints both. Each question's `eval.task` event says where it falls, so the
  stats count it from the eval journal without reading results. An evaluation runs for hours, so
  the stats read yesterday's events for one that started before midnight. The queue is words from
  a fixed list and model names that pass the same check as everywhere else.
- **A pause stops the work at once.** Vlad, the same night: "i need it to pause even when it's mid
  task, when i tell it to pause it drops, it does not wait to finish". The harness's work runs in
  a context a pause cancels within five seconds of being asked for, mid-test or mid-question, while
  the harness itself keeps running and waits the pause out. Nothing is lost but the piece in
  progress: an evaluation carries on from its checkpoint, a bench's request stays in the inbox and
  it runs again from the start, a retest queues the servers it hadn't reached, and a tool test a
  pause cut short is tested again. The event says `eval.paused` or `bench.paused`, apart from a
  stop, and `lately` says paused.
- **A model can be skipped.** Vlad, 28 November 2026, with granite-4.2-30b part way through: "can
  we add a way to skip a model?". The Lab tab's Skip (or `anchor-lab skip -model <name>` on the
  box) sends the signed `skip` command, which leaves a skip request in the inbox like the others
  (no sudo, the harness acts on it itself). The evaluation or bench of that model in progress stops
  within five seconds (`skipEvery`), the ones waiting for it are dropped, and the lab moves on.
  Only what was asked for before the skip goes: an evaluation asked for after it runs as usual.
  The questions done stay in the checkpoint, so evaluating it again the same way within the week
  carries on from them. The events say `eval.skipped` or `bench.skipped`, and `lately` says
  skipped. Retests and the explores with the lab's own model aren't skipped: switching the model
  is how those change.
- **A model whose questions take over 10 minutes on average isn't tested.** Vlad, the same
  evening: "it's very slow and it failed a few things already so maybe if the task takes more than
  10 min on average we can classify it as not to be tested", "and have a way to force it maybe".
  An evaluation times each question it writes down (`eval.SlowWatch`, through `Options.After`),
  the ones in its checkpoint included, and leaves out the ones the site or the box failed under,
  since those measure them and not the model. Once three are in (`eval.SlowAfter`, so one cold
  first question doesn't decide it) and the average is over 10 minutes (`eval.SlowQuestion`), it
  stops, keeps its checkpoint, and writes the model down in `<state>/eval/slow/<model>.json` with
  the average. A later request for that model is dropped with `eval.slow` unless it's forced: the
  Lab tab's "Evaluate anyway" sends `eval` with `force`, which puts "force" on the request's third
  line (`journal.QueueEvalForced`, the normal set in either mode), and `anchor-lab eval -force`
  does the same. A forced evaluation runs to the end however slow it is, and any evaluation that
  gets its score takes the mark off. The stats carry the marked models in `slow` and `lately`
  says slow. The time per question is the task's own wall time, not the time it waited behind a
  pause, so a paused night doesn't make a model slow. Adding skipped and slow to `lately` and
  `slow` to the stats keeps `anchor-lab-stats/1`: nothing an older field meant changed, the
  same way paused was added, and the site takes the new words in the same change.
- **A pause, for the night.** Vlad, 18 November 2026: "i need an option to pause it the lab since
  it's creating too much heat in the room overnight and people can't sleep, i want to pause it for 8
  hours as an option". `anchor-lab pause -for 8h` (8 hours unless told otherwise), or the Lab tab's
  button through the signed `pause` command, writes `inbox/pause` holding the time it ends;
  `anchor-lab pause -off` or the `resume` command removes it. The harness looks before each test,
  each bench server and each evaluation question, and (the entry above) stops the one in
  progress, then goes on by itself where it stopped, with nothing to queue again. It's a file in the inbox like
  every other request, so it needs no sudo. A pause is at most 24 hours, and a file further off
  than that, already past or not a time is no pause, so a mistake can't stop the lab for ever. The
  model's server stays up, idle: the heat is the work, and loading the model again in the morning
  would only cost time.
- **A stop isn't a failure.** On 18 November 2026 an install stopped qwen3.8-27b's evaluation part
  way, and the log called it `eval.failed`, the same as a model that broke. Vlad agreed it should
  say so: "yeah, we should do that". When the harness is stopping (a restart or an install), an
  evaluation or a bench that ends early logs `eval.stopped` or `bench.stopped`, and the harness
  takes no more requests. The stats carry the latest endings, done, failed or stopped, newest first
  in `lately`, and a harness that starts again while one was running counts it as stopped even
  when nothing was logged. `anchor-lab status` prints the latest three, so the model to queue again
  is in front of whoever installed.
- **The model chooses: its own tool calls, or programs.** Vlad, 18 November 2026: "can we test
  it's tools call capability as well not just writing the code, for something it will be writing
  code and executing but let the model choose how it wants to get there, tool call or write code".
  In either mode (`-mode either`, the default) the model is offered the MCP server's callable
  tools and `lab_fetch` as native tool calls, in the OpenAI chat format llama-server speaks, and
  one more, `run_program`, which builds and runs a Go program as code mode does; a go block in
  the reply's text runs too. Each reply calls tools, runs a program, or answers. Native calls go
  through the same gateway as a program's, so the limits, the forced dry runs, the redaction and
  the address check are the same, and nothing new reaches the network. A native call's answer
  reaches the model cut at 24,000 characters (12,000 until 22 November 2026), where a program
  reads up to 4 MB and prints what it needs: that's the trade the model chooses between. Either mode gets twice code mode's replies a task (16 to
  8 then, 32 to 16 since 21 November 2026), since a native call usually takes a reply of its own. Asked which should be the Lab
  score, Vlad chose either mode, since that's how agents work: the queue's requests run in it,
  and code mode is a run asked for by hand (`-mode code`, its own request in the inbox). Scores
  keep their mode and aren't compared across modes; a score from before modes is code mode. Each
  task records how the model got there (native, program, both or none) and a score sums them in
  `paths`, so which path a model takes, and whether it pays on the hard tasks, is in the record.
  A request with tools that fails is tried once more with them, since a malformed tool call is
  often bad luck in the sampling; if it fails again, at any turn, the task carries on with
  programs only, the conversation so far rewritten as plain text (what was called, what came
  back) with no two turns of the same role in a row, and the score counts it in `noTools`. A
  model server whose chat template takes no tools lands there on the first turn. Vlad, 18
  November 2026: "if a model fails with the tool call we can test with the code call, we should
  try to get it to do the thing". So the first native call in a task that comes back with an
  error also says, once, that a program can make the same call. A model isn't marked down for
  its server, and one whose own tool calls don't work still gets the chance to answer. The checkpoint keeps the mode, and isn't carried on in the other.
- **A stopped evaluation carries on where it stopped.** Vlad, 18 November 2026: "can we add a thing
  to be able to stop and continue mid eval? this way we don't waste that much time every time, like
  now i cna;'t deploy for another few hours until the eval run i started at 6 finsihes". Each
  question is written to `<state>/eval/resume/<model>.ndjson` as it finishes (`eval.Checkpoint`),
  on disk before the next one starts. A stop keeps that file, and the request stays in the inbox,
  still the oldest, so the next start takes it first and asks only what's left: a stop costs the
  question in flight, about three minutes, not the hours before it. It's safe to carry on because
  each question's right answer is worked out from the site when that question is asked, never
  once for the whole evaluation. The score keeps the first start, counts the time it ran and not
  the time it waited, and says in `resumed` how many times it was stopped; `harness` is the build
  that finished it. A checkpoint of another task set, another number of passes, or older than seven
  days is dropped and the evaluation starts again (`eval.restart` says why), since a score made of
  passes weeks apart says more about the weeks than the model. The stats' progress counts on from
  `eval.resume`, which carries what was done before the stop.
- **Time is part of the benchmark, as its own number.** Vlad, 18 November 2026: "we should record
  everything and the time taken should be part of our benchmark", and asked whether time should
  count inside the Lab score or beside it, beside it. A score carries the seconds a question takes
  on average (from opening the server to the grade, over the tasks that ran), output tokens a
  second of the model's time, the time in the cells, and each task's own time. The Lab score stays
  what the model got right, so a fast wrong answer and a slow right one aren't folded into one
  figure; the site shows both and lets a reader weigh them.
- **Three passes, flips and a sample, as the bench.** A task is right when it's right in more than
  half the passes it was scored in. `-sample` runs ten tasks once and keeps nothing, for a quick
  look at a model before the hours the whole set takes. Scores stay on the box in `eval.ndjson`
  and its runs in a journal of their own; `/v1/evals` serves each model's latest, signed, numbers
  and task ids only, for the site's results page.
- **The lab is a repository of its own, and public.** It imports nothing from the site and the site
  imports nothing from it, so the binary on the box has none of the site's code in it. The site's
  collector is written against the formats (`anchor-lab-result/1`, `anchor-lab-stats/1`), not
  against this code. It's public so that how a tool is tested can be read, and a vendor can run it
  against their own server.
- **The targets come from Anchor Terminal's public API.** `anchor-lab targets` reads
  `/api/v1/tools.json` and keeps the listings whose hosted MCP server answered the site's own probe
  without a key. That's the set where no account is needed to list the tools, so the first version
  carried no credentials at all; a key we're given now comes in sealed (below). Reading the public API, not the site's files, means anyone can make the same
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
- **The harness is kinder to a model without making a question easier.** Three hosted models
  took the evaluation by hand on 21 November 2026 and each scored 100 where the best model on the
  box scores 98. Vlad: "can we impove the harness as well, is hiku that much better than qwen 3.8
  flash next? or is this harness better than the one we have written for anchorlab?". The loop
  they ran in was kinder than ours in ways that aren't about the questions: one conversation for
  a whole pass, no limit on replies, Python with a result straight away, files to keep things in,
  and the limits said up front. Three of those are cheap to give a model on the box, and none
  touches a task or how an answer is checked, so the set's version stays. First, the model is
  told its limits (`limitsNote`): the replies it has and the calls and fetches a question gets,
  where before it learned them by being refused. Second, a build that failed only on an import
  or a variable nobody used is repaired and built once more (`RepairUnused`), without the model
  spending a reply on it. Go refuses such a program, and the mistake says nothing about whether the
  model can answer (how often it happens on the box, `anchor-lab why` will now say). The
  repair is only made when the compiler said nothing else, the repaired program still has to
  build, and the model is told it happened. Third, `anchor-lab why -model <name>` reads the
  journal and says how each lost question was lost (`eval.Why`), because until now the only way
  to know was to read transcripts, and the next change to the harness should come from what the
  box says and not a guess. An `eval.end` event now carries the replies allowed, the builds, the
  failed ones, the repairs, the lookups and whether a ruled-out tool was called. What this costs:
  a score made on this harness isn't quite comparable with one made before it, which is why a
  score carries its harness's version. Left for a decision of their own: a scratch file a
  question's programs can share, and Python in the cell beside Go, which is probably the biggest
  help to a small model and the biggest change to the cell.
- **The limits are there to protect a server, not to trip a model.** Told that the hard set's
  toughest questions were the ones that didn't fit a question's 40 fetches, Vlad, 21 November
  2026: "i would increase the limits not reduce them, i want the model to do its best to tests the
  tools we are listing". And the next morning, of the first numbers: "we can use more token,
  don't need that many tries but tokens we can use more". The numbers had been set low for the
  first runs and never looked at again, and some could only ever measure themselves: 40 calls a
  run can't try every tool once on a server that lists 53. So a run's calls now grow with the
  server, three tries at every tool the lab will call and 20 over, from 60 up to 400
  (`gateway.CallBudget`). Tries at one tool stay at three: a tool that hasn't answered in three
  goes isn't going to on the fifth, and each try is a call on somebody else's server. What goes
  up is what a model has to think and read with. A reply may be 16,384 tokens where it was 4,096,
  and thinking 8,192 of them where it was 2,048 (`reasoning-budget` in the presets). A direct
  call's answer reaches the model to 24,000 characters where it was 12,000, a program's output to
  12,000 where it was 6,000, and a compiler's complaint to 8,000 where it was 4,000. An explore
  gets 12 replies where it had 6, a bench server 16 where it had 8, an evaluation question 32 in
  either mode and 16 in code mode where it had 16 and 8, and 200 calls and 200 fetches where it
  had 40 of each, since the site it asks is ours. What didn't move is what protects somebody
  else's server or the box: 300 ms between two requests, one address a run, a tool that changes
  something never called, 60 s a call, 4 MB an answer, and a cell's time and memory. A model that
  needs fewer replies uses fewer, so the cost is only paid by the runs that were being cut off.
  Two things follow. Scores made before and after aren't comparable, like any change here, so the
  rerun that carries the NONE fix carries this too. And the hard set's questions that didn't fit
  40 fetches fit 200: they stay, as long jobs over many listings where the quicker way is still
  to ask the search tool, and the scores the hosted models got on them by hand were made under
  the old limit.
- **A conversation is cut to fit the context, not refused.** More replies and more to read in
  each means a long task can outgrow a model's context: eight calls in one reply, each read to
  24,000 characters, came close to the 65,536 tokens every model had on their own. When that
  happened the model's server refused the request and the task ended with no answer, which read
  in the scores as the model giving up. So the model client knows the context it's working in
  (`brain.Client.Window`) and, when a conversation wouldn't leave room for the reply, sends it
  shorter (`brain.Fit`). The oldest tool output and program output go first, each cut to its
  first 300 characters with a note of how much is gone so the model knows to ask again, then the
  oldest programs and replies. The latest four messages are what the model is working from, so
  they go last, by the same share each, and only as far as it takes. The system message and the
  task are never cut. The harness keeps the whole conversation in the run's folder: only what's
  sent is shortened, and the journal says when and by how much (`model.trimmed`). Tokens are
  counted the way the server last counted them, the characters it was sent over the prompt
  tokens it reported, and never at more than three characters each, which is fewer than prose or
  JSON take, so a first guess errs towards leaving room. A request the server still refuses is
  sent once more into half the room, when that cuts anything more.
- **The model's server decides each model's context, and the harness asks.** Vlad, 22 November
  2026, told that a longer context was memory only the box could measure: "let's add to our
  harness the bigger context when we start the models". Where a model starts is the server's
  presets, and a number there is a guess: the cache a context needs depends on a model's layers
  and heads, not on the size of its file, and the largest models leave about 20 GB of the GPU's
  100. A context that doesn't fit is a model that doesn't load. llama.cpp already measures this
  when it loads a model (`--fit`) and takes the largest context there's room for, so the presets
  now set none: `fit-ctx = 65536` is the least a model may be given, which is what every model
  had, and `fit-target = 4096` leaves 4 GB of the GPU free. Pinning every model at 131,072 was
  the other way and was turned down, because it's right only for the models it happens to fit.
  The harness can't read the presets (they're root's and the model server's) and shouldn't have
  to be kept in step with them, so it asks the server: `GET /props?model=<name>` says what the
  model was loaded with (`brain.Client.Loaded`). It uses that up to 131,072 tokens
  (`ANCHOR_LAB_CONTEXT`), a ceiling so that a model given a million tokens doesn't spend minutes
  a reply rereading one long task, and so that the models with room all work in the same
  context. A server that doesn't say (one that isn't llama.cpp) is taken to have 65,536. An
  evaluation and a bench ask before their first question, which loads the model, and write down
  what they got (`model.context`). A model the server can't load ends the run there with one
  event (`model.unloaded`), where it used to fail every question in turn and end as "no program
  reached a cell". The answer is kept until a request fails, since a model the server loads
  again may be given a different context. None of this has run on the box: what `--fit` makes of
  this GPU's memory is the first thing to look at after an install, and a `c` line in a model's
  section is the way back for that model.
- **Requests run in the order they were asked for.** A bench or evaluation request carries the
  time it was made, in nanoseconds, and the harness takes them oldest first by that, then by name.
  It used to go by the file's own time, which is only as fine as the filesystem's clock tick: seven
  evaluations queued in one loop on 18 November 2026 got the same time and ran in name order. A
  model asked for again while it waits keeps its place. Vlad: "yeah do a small pr we'll do it after
  the evals finish".
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
- **Keys only the box can open.** Many servers on the list run no tool without an account. The
  first version left them out; now a key we're given goes to the box sealed, and nothing but the
  harness on the box can open it. The box makes an ECDH P-256 key pair once; the private half is a
  file only root reads, handed by systemd to the harness's service alone, so no person in the
  anchorlab group, no other service and no cell can read it. The admin's browser seals a key and
  how it's sent to the box's public half with WebCrypto (ECDH, HKDF-SHA256, AES-256-GCM, all in Go's
  standard library on this side), binding it to the listing, its server's address and the box's
  key. So the site holds only what it can't open, a copy sealed for one server can't be replayed
  against another, and one sealed to another box won't open. The gateway sends it to that server
  on every request of the run and nowhere else, never to the model or the program, and blanks it
  out of anything the server sends back. OAuth client credentials are swapped for a token by the
  gateway, through the same address check. A result made with a key says so, since its numbers are
  what an account gets.
- **The site drives the lab, with signed commands.** Store or delete a sealed key, test some
  servers again, the failures or all of them with a model, bench a model, switch the model: each
  is a command to the stats service, signed with HMAC-SHA256 over the version, the time and the
  body, with a secret only root reads on the box (handed to the stats service alone) and kept in
  the site's settings. A command over a minute old or seen before is refused. A command only
  writes a file in the inbox; the harness acts on it as itself, as it does on a person's. None
  carries code or an address: the servers to call still come from the public list, and a key only
  ever goes to the server it was sealed for. The service that takes the commands can store a
  sealed key and can't open one.
- **The models list carries each model's size.** Vlad, 18 November 2026, of the Lab tab's models
  table: "can we show models size as well? in GB?". `modelSizes` in the stats is the bytes of each
  model the server offers that models.txt lists, read from the list built into the harness
  (package models): the size the Hub gave, which fetch.sh checks a download against, so the stats
  service needs no read of the models folder. A model from a preset of our own isn't in the list
  and has no size. It's a field added beside the others, so `anchor-lab-stats/1` stays: nothing
  that was there changes meaning.
- **The box signs what it tells the site.** With the command secret set, the stats, the results
  and a transcript carry an HMAC over the version, the time, the path and the body. The stats are
  where the site learns the box's public key, so an unsigned one would let another machine on the
  local network hand the site its own key and collect every key sealed to it; signed, it can't. The
  public half lives beside the private half in `/etc/anchor-lab`, root's folder, so no process
  that reads strangers' text can change which key the site seals to. The fingerprint is printed by
  `install.sh` and shown on the admin page, for a person to compare once.
- **A run can be read from the admin page, and isn't kept there.** The transcripts used to be for
  a person on the box only. `GET /v1/run`, signed like a command, sends one run's tool list,
  programs, outputs, model replies and events, found from the run's own result line rather than a
  path the asker gives. The site passes it to an admin's browser as plain text and keeps none of
  it, and it never goes into a prompt. Results and the stats still carry only numbers and words
  from fixed lists.
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
- **Keys in plain text anywhere, the site included.** Each key is something to steal and something
  that can spend. They were left out of the first version for that reason; they came in sealed to
  the box, so a site's database or backup that leaks gives nothing usable.

## Numbers

| What | Value | Where |
| --- | --- | --- |
| Calls a run on a server may make | 60 to 400: three tries at every tool the lab will call, and 20 over | `gateway.CallBudget`, `DefaultLimits.MaxCalls`, `MaxCallBudget` |
| Calls to our own MCP server, and fetches of our site, for one evaluation question | 200 each | `eval.TaskCalls` |
| Replies asked of the model on one evaluation question | 32 in either mode, 16 in code mode | `eval.EitherTurns`, `eval.CodeTurns` |
| The most a reply may be, and the most of it that may be thinking | 16,384 tokens, 8,192 | `-max-tokens`; `reasoning-budget` in `models/presets.ini` |
| What a model reads of a direct call's answer, a program's output, a compiler's complaint | 24,000, 12,000 and 8,000 characters | `nativeClip` in `eval/either.go`; `runProgram` |
| A model's context | what llama.cpp has room for when it loads it, 65,536 tokens at least | `fit-ctx`, `fit-target` in `models/presets.ini` |
| The most of it the harness uses | 131,072 tokens | `ANCHOR_LAB_CONTEXT`, `maxContext` |
| One answer's size | 4 MB | `MaxBody` |
| One call's time | 60 s | `CallTimeout` |
| Between two requests to a server | 300 ms | `MinInterval` |
| A call's arguments | 64 KB | `MaxArgs` |
| A tool's answer as the program sees it | 32 KB | `MaxText` |
| Replies asked of the model in one explore | 12 | `MaxTurns` in `cmd/anchor-lab/main.go` |
| Tries at one tool | 3 | `Attempts` |
| A program's build, and its run | 3 min, 90 s (2 min at most) | `buildLimit`, `JobTimeout`, `maxRunLimit` |
| A program, and what's kept of its output | 256 KB, 64 KB | `maxSource`, `maxOutput` in `cell.go` |
| A cell's memory, processes, life | 6 GB, 512, 7 min | `anchor-lab-cell@.service` |
| A server's test stands for | 7 days | `-retest` |
| Between two servers | 20 s | `-pause` |
| Targets from the listings alone, 14 November 2026 | 61 | `anchor-lab targets -data data` |
| Bench cases: callable, traps, servers | 43, 7, 5 | `bench/cases.go`, version `anchor-lab-bench/1` |
| Replies asked of the model on one bench server | 16 | `MaxTurns` in `benchCmd` |
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
- Run by hand, 21 November 2026: Claude Haiku 5.5, Sonnet 5.5 and Fable 5.2 through both sets,
  three passes each. Every pass was a new instance with no history, given what `eval -questions`
  prints and a logged runner and nothing else, and graded with `eval -grade` against a snapshot
  written before the batch started (Decisions, "A model the box can't run gets the same
  grading"). None of it is in `eval.ndjson`, and none of it was made in the harness: one
  conversation ran a whole pass, in Python, with no limit on replies.
  - The normal set, 59 questions. All three got 59 of 59 with the passes counted together, a Lab
    score of 100. Fable was right in every pass on all 59, Haiku on 55 and Sonnet on 51. Nobody
    gave a wrong answer in nine passes: every miss in a single pass was a right answer with no
    lookup counted behind it, either a file reused from an earlier question (which a model on
    the box can't do) or a 404 as the only lookup on a NONE trap (the flaw the set's second
    version fixes).
    The best local model's Lab score is 98, so the normal set no longer tells the top apart. A
    pass over the 59 took between two and a half and ten minutes from its first call to its last.
  - The hard set, 66 questions, with the lab's limits enforced by the runner. Fable 66, 66 and
    66 right, a score of 100. Sonnet 62, 64 and 64, 65 right with the passes counted together,
    98. Haiku 64, 62 and 55, 63 right, 95, and right in every pass on 53. What cost them: the
    licence count over 60 listings that doesn't fit one call budget (Haiku left it blank three
    times), leaders counted with unranked listings among them, a grade worked out without taking
    the deduction off, a `license` read from the file's own metadata instead of the listing's,
    and in one of Haiku's passes nine right answers with no lookup of their own. One question
    was reworded after these runs: "whose listings record the MIT licence" let a listing under
    "MIT and Apache-2.0" in, Fable read it that way three times, and the key didn't. Either
    reading is counted right in the numbers above. Held to the key as it was, Fable is 65 of 66
    and 98, and Haiku 93. Two ratio questions and the three licence questions were reworded for
    the same reason, with no change to their answers.
  - The first two versions of the hard set, 44 and then 57 questions, were run on Haiku alone:
    44 of 44 in all three passes, then 57, 55 and 56 of 57.
  - Not run: the hard set by any model on the box, or any hosted model inside the harness, which
    refuses a model that isn't on this machine. Until the box has run it, the hard set's
    difficulty for a local model is a guess.
- Not run: the units on a real machine, and `explore` with a real model against a real server. The
  first run on the box is the test of both. The likeliest thing to need a change is the cell
  unit's system call filter, which fails closed: a cell that can't build says so in the journal.

## Open

- **What grading the saved runs again under version 3 changes.** `eval.Rescore3` is tested on
  made-up journals. On the box, `anchor-lab eval -rescore` says what each model loses before the
  harness that keeps the new scores is installed. A score can only go down or stay, apart from a
  question the box failed under leaving it, which changes what the score is out of.
- **What grading the eleven saved runs again changes.** `eval.Rescore` is tested on made-up
  journals, not on the box's. The four traps are level 2, so a model can gain up to 8 of the 110
  weighted points. `anchor-lab eval -rescore` on the box says what each model gains, before the
  harness that keeps the new scores is installed.
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
- **HTTP APIs, the rest.** The first item of this list is from before the lab could test one. It
  now reads an OpenAPI document in JSON or YAML and sends GET requests with path and query
  parameters (Decisions, "A plain HTTP API is tested the way an MCP server is" and "The harness
  reads YAML itself"). Still to do: the site's list saying which listings are APIs and where
  their description is, and the site taking their results. Keys, which is most APIs worth testing.
  A server address with variables in it, which the lab refuses and 390 of the 4,103 descriptions
  read from the APIs.guru directory have, though the specification gives each variable a default
  to use. Header parameters. A description split over several files. A way to test a POST that
  only reads, such as a search, without sending one that writes: 395 of those 4,103 have no GET
  operation at all. And an API with no description, where the model would have to read the docs.
