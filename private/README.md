# private/

Everything in this folder except this file and `settings.example.env` is ignored by git
(`.gitignore`), so it never reaches the public repository. It's where our own things go:

- `results/`: what the lab found, copied off the box: `results.ndjson`, the event files, the bench
  scores, and the transcripts (`runs/<run>/`) when someone wants to read them. The transcripts hold
  servers' tool descriptions and models' words, which never leave the box on their own and never
  get published.
- `settings.env`: our own settings, read by `build.sh`, `models/fetch.sh` and `models/install.sh`
  when it's here. Start from `settings.example.env`.
- `presets.ini`: our own model presets. `models/install.sh` uses it in place of
  `models/presets.ini` when it's here.
- Anything else that's ours and not for the public: notes, a copy of the private bench set.

Why it's in the repository at all: one checkout holds both the public code and the private things
next to it, and the scripts find them without being told where. Why it's ignored: the repository
is public, and none of this is. A file here that should be public moves out of `private/` and
goes through a pull request like any other.
