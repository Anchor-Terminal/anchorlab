# Working in this repository

The harness Anchor Terminal tests the tools it lists with. README.md is how to run it, DESIGN.md is
why it's built this way. This repository is public: nothing about our own machines goes in it (no
addresses, hostnames, users or paths of the lab box or the site's server), and neither does the
private bench set.

- Go, standard library only. No dependency gets added.
- `go test -short ./...` is quick; `go test ./...` also compiles and runs real programs. Run both
  before a pull request, and `./build.sh` once.
- The private bench set lives in Anchor Terminal's own repository (`tools/lab/`). `build.sh <file>`
  copies it into `bench/` as `zz_private.go` for one build and removes it after. Never commit a
  `bench/zz_private*.go`; `.gitignore` names them. The public sample is `bench/set_sample.go`.
- The formats are contracts with the site: `anchor-lab-result/1` (`journal/journal.go`),
  `anchor-lab-stats/1` (`stats/stats.go`), `anchor-lab-targets/1` (`targets/targets.go`). A change
  to what a field means is a new version, and the site's side changes in the same breath.
- What leaves the box in a result or the stats is numbers and words from fixed lists. A field that
  would carry a tool's description, an error message or a model's words doesn't go in either. The
  one exception is a run's transcript, sent signed to the site on request for an admin to read,
  and kept nowhere off the box.
- A key we're given for a server is sealed to the box (`seal/`) and opened only by the harness.
  Never write one in plain text in a file, a log, a test fixture or a command, and never let the
  model, a program or a result see it.
- Every change goes on its own branch, `claude/<short-slug>`, and lands through a pull request that
  Vlad or Quynh merges. Never push to main. Base every pull request on main, never on another
  pull request's branch: a stacked one merged after its base goes into a branch that's deleted,
  and never reaches main (#4 did, on 17 November 2026).
- Commits and pull requests are in Vlad's name: author `Vlad Cealicu <me@vladcealicu.com>`, no
  `Co-Authored-By` or session lines in commit messages, and no "Generated with" footer in a pull
  request's description.
- `private/` is ignored by git, apart from its README and `settings.example.env`. Results,
  transcripts, our settings and anything else that's ours go there, never anywhere else.
- MODELS.md and `models/` are how the models run. A new model or build is a line in
  `models/models.txt` with its size and SHA-256 from the Hub, a section in `models/presets.ini` with
  its publisher's sampling, and a row in MODELS.md.
- The box builds from a clone of this public repository (`git pull`, `./build.sh`, `install.sh`).
  Nothing private is cloned there; the private bench set is one file in its `private/`.
- The list of servers to test comes from Anchor Terminal's public API (`anchor-lab targets`), and
  what the lab finds is filed as `research/<model>/<company>/<tool>/<run>/` (`journal.Folder`).
- Debian first, on the box and in every instruction here. Install from Debian's own archive
  (trixie, then trixie-backports) and nothing else unless there's no way round it: no third-party
  apt repositories, no install scripts piped to a shell, no pip, npm or snap, no downloaded
  binaries. When something has to come from elsewhere, DESIGN.md says what and why. Today that's
  llama.cpp, built from its source, and the model files from Hugging Face.
- No sudo, apart from installing (`install.sh`, `models/install.sh`). Running the lab works as an
  ordinary user in the `anchorlab` group: what a person does to a running lab is a file in its
  inbox that the harness acts on as itself. A new command that needs root to use is a design to
  change, not a `sudo` to add to the docs.
- Decisions go into DESIGN.md in the same pull request, with the reason.
