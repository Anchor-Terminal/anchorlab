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
- **What the lab is doing shows where Vlad looks.** Vlad, 18 November 2026, of the admin page's Lab
  tab: "i should see what it's doing on the lab page, that's where i wanted it". The stats carry
  the evaluation in progress (which question of which pass, how many right, wrong and skipped so
  far, when it started) and the queue in the inbox, in the order the harness takes it, and
  `anchor-lab status` prints both. Each question's `eval.task` event says where it falls, so the
  stats count it from the eval journal without reading results. An evaluation runs for hours, so
  the stats read yesterday's events for one that started before midnight. The queue is words from
  a fixed list and model names that pass the same check as everywhere else.
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
  reaches the model cut at 12,000 characters, where a program reads up to 4 MB and prints what it
  needs: that's the trade the model chooses between. Either mode gets 16 replies a task to code
  mode's 8, since a native call usually takes a reply of its own. Asked which should be the Lab
  score, Vlad chose either mode, since that's how agents work: the queue's requests run in it,
  and code mode is a run asked for by hand (`-mode code`, its own request in the inbox). Scores
  keep their mode and aren't compared across modes; a score from before modes is code mode. Each
  task records how the model got there (native, program, both or none) and a score sums them in
  `paths`, so which path a model takes, and whether it pays on the hard tasks, is in the record.
  A model server whose chat template takes no tools refuses a request with tools in it; the task
  then carries on with programs only and the score counts it in `noTools`, so a model isn't
  marked down for its server. The checkpoint keeps the mode, and isn't carried on in the other.
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
