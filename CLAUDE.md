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
- What leaves the box is numbers and words from fixed lists. A field that would carry a tool's
  description, an error message or a model's words doesn't go in a result or the stats.
- Every change goes on its own branch, `claude/<short-slug>`, and lands through a pull request that
  Vlad or Quynh merges. Never push to main.
- Commits and pull requests are in Vlad's name: author `Vlad Cealicu <me@vladcealicu.com>`, no
  `Co-Authored-By` or session lines in commit messages, and no "Generated with" footer in a pull
  request's description.
- `private/` is ignored by git, apart from its README and `settings.example.env`. Results,
  transcripts, our settings and anything else that's ours go there, never anywhere else.
- MODELS.md and `models/` are how the models run. A new model or build is a line in
  `models/models.txt` with its size and SHA-256 from the Hub, a section in `models/presets.ini` with
  its publisher's sampling, and a row in MODELS.md.
- The list of servers to test comes from Anchor Terminal's public API (`anchor-lab targets`), and
  what the lab finds is filed as `research/<model>/<company>/<tool>/<run>/` (`journal.Folder`).
- Decisions go into DESIGN.md in the same pull request, with the reason.
