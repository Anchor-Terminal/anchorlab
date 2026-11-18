// Package stats is what the lab tells the site's admin page about itself: what it's testing now,
// how today has gone, its latest results and how the machine is doing. `anchor-lab stats` serves
// it over HTTP to the site's server and nobody else (DESIGN.md, "The admin page sees the
// lab").
//
// Only numbers and words from fixed lists go out, the same rule as results.ndjson: a slug, a task,
// a status, the model's name from our own config, counts and times. No tool's name or description,
// no error message, no sentence a model wrote.
package stats

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/Anchor-Terminal/anchorlab/journal"
	"github.com/Anchor-Terminal/anchorlab/seal"
	"github.com/Anchor-Terminal/anchorlab/targets"
)

// Version names the format. A change to what a field means is a new version, and the site refuses
// one it doesn't know.
const Version = "anchor-lab-stats/1"

// Recent is how many of the latest results go out.
const Recent = 20

// stale is how long a run may go without an event before it isn't called current. A model's reply
// may take up to 15 minutes (-model-timeout) with nothing written meanwhile.
const stale = 20 * time.Minute

// Stats is the whole answer.
type Stats struct {
	V          string    `json:"v"`
	At         time.Time `json:"at"`
	Harness    string    `json:"harness"`
	Model      string    `json:"model"`             // ANCHOR_LAB_MODEL, "" when only connect runs
	ModelUp    *bool     `json:"modelUp,omitempty"` // the model's server answered /health; nil when there's no model
	LastEvent  time.Time `json:"lastEvent,omitempty"`
	Started    time.Time `json:"harnessStarted,omitempty"` // the harness's last start today
	Targets    int       `json:"targets"`
	Current    *Current  `json:"current,omitempty"`
	Benching   string    `json:"benching,omitempty"`   // the model the harness is benching now, asked for through the inbox
	Evaluating string    `json:"evaluating,omitempty"` // the model the harness is evaluating now (package eval)
	Today      Today     `json:"today"`
	Results    int       `json:"results"` // lines in results.ndjson
	// Box is the box's public key, which the admin's browser seals a server's key to (package seal)
	Box *Box `json:"box,omitempty"`
	// Keys are the sealed keys waiting in the inbox: which listing, which box key, since when.
	// Never the key: this service can't open them.
	Keys []KeyHeld `json:"keys"`
	// Models are what the model server offers, for the site's model picker
	Models []string `json:"models,omitempty"`
	Recent []Result `json:"recent"`
	System System   `json:"system"`
}

// Current is the run in progress.
type Current struct {
	Run       string    `json:"run"`
	Slug      string    `json:"slug"`
	Started   time.Time `json:"started"`
	Stage     string    `json:"stage"` // connecting | exploring
	Turn      int       `json:"turn"`
	Calls     int       `json:"calls"`
	Errors    int       `json:"errors"`
	LastEvent time.Time `json:"lastEvent"`
}

// Today is the UTC day so far, from the events and the results.
type Today struct {
	Runs         int            `json:"runs"`
	Connected    int            `json:"connected"`
	ConnectFails int            `json:"connectFails"`
	Calls        int            `json:"calls"`
	CallErrors   int            `json:"callErrors"`
	Turns        int            `json:"turns"`
	ModelFails   int            `json:"modelFails"`
	Programs     int            `json:"programs"`
	BuildFails   int            `json:"buildFails"`
	CellFails    int            `json:"cellFails"`
	ModelMs      int64          `json:"modelMs"`
	PromptTokens int64          `json:"promptTokens"`
	OutputTokens int64          `json:"outputTokens"`
	Outcomes     map[string]int `json:"outcomes"` // explore results today: success | partial | failure | none
	Keyed        int            `json:"keyed"`    // of those, servers that answered every call with "you need a key"
	Statuses     map[string]int `json:"statuses"` // connect results today: ok | auth | unreachable | refused | error
}

// Result is one finished run, cut down to what the admin page shows.
type Result struct {
	Run        string    `json:"run"`
	Slug       string    `json:"slug"`
	Task       string    `json:"task"`
	Model      string    `json:"model,omitempty"`
	Ended      time.Time `json:"ended"`
	Status     string    `json:"status"`
	Outcome    string    `json:"outcome"`
	Tools      int       `json:"tools"`
	ToolsTried int       `json:"toolsTried"`
	ToolsOK    int       `json:"toolsOk"`
	Calls      int       `json:"calls"`
	Errors     int       `json:"errors"`
	P50Ms      int       `json:"p50Ms"`
	P95Ms      int       `json:"p95Ms"`
	WallMs     int64     `json:"wallMs"`
}

// System is the machine.
type System struct {
	UptimeS  int64      `json:"uptimeS"`
	Load     [3]float64 `json:"load"`
	CPUs     int        `json:"cpus"`
	MemTotal int64      `json:"memTotal"` // bytes
	MemAvail int64      `json:"memAvailable"`
	SwapUsed int64      `json:"swapUsed"`
	Disks    []Disk     `json:"disks"`
	GPU      *GPU       `json:"gpu,omitempty"`
	CPUTempC float64    `json:"cpuTempC,omitempty"`
}

// Disk is one filesystem, by the folder we asked about.
type Disk struct {
	Path  string `json:"path"`
	Total int64  `json:"total"`
	Free  int64  `json:"free"`
}

// GPU is the AMD GPU as amdgpu's sysfs files have it.
type GPU struct {
	BusyPct   int     `json:"busyPct"`
	VRAMUsed  int64   `json:"vramUsed"`
	VRAMTotal int64   `json:"vramTotal"`
	GTTUsed   int64   `json:"gttUsed"`
	GTTTotal  int64   `json:"gttTotal"`
	TempC     float64 `json:"tempC,omitempty"`
	PowerW    float64 `json:"powerW,omitempty"`
}

// Config is where to look.
type Config struct {
	State    string   // /var/lib/anchor-lab
	Disks    []string // folders whose filesystems are reported: /, the state, the models
	Model    string   // ANCHOR_LAB_MODEL
	ModelURL string   // ANCHOR_LAB_MODEL_URL; /health beside it is asked, on this machine only
	Harness  string   // this binary's version
	BoxPub   string   // the box's public key, /etc/anchor-lab/box.pub: root's to write; "" is <State>/box.pub
	Root     string   // "" in use; a folder standing in for / in tests (proc, sys)
	Now      func() time.Time
}

var slugOK = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,79}$`)
var runOK = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}-[a-z0-9][a-z0-9-]{0,79}$`)
var modelOK = regexp.MustCompile(`^[A-Za-z0-9._:/+-]{1,96}$`)

var tasks = set("connect", "explore")
var statuses = set("ok", "auth", "unreachable", "refused", "error")
var outcomes = set("success", "partial", "failure", "none")

func set(s ...string) map[string]bool {
	m := map[string]bool{}
	for _, x := range s {
		m[x] = true
	}
	return m
}

// word is s when it's in the list, otherwise "other": a field from a fixed list never carries
// anything else out.
func word(s string, ok map[string]bool) string {
	if ok[s] {
		return s
	}
	return "other"
}

// Collect reads the state and the machine now.
func Collect(c Config) Stats {
	now := time.Now
	if c.Now != nil {
		now = c.Now
	}
	t := now().UTC()
	s := Stats{V: Version, At: t, Harness: c.Harness, Today: Today{Outcomes: map[string]int{}, Statuses: map[string]int{}}, Recent: []Result{}}
	model := c.Model
	if m, ok := journal.InboxModel(c.State); ok {
		model = m
	}
	if modelOK.MatchString(model) {
		s.Model = model
	}
	if f, _, err := targets.Load(filepath.Join(c.State, "inbox", "targets.json")); err == nil {
		s.Targets = len(f.Targets)
	}
	finished := results(c.State, t, &s)
	events(c.State, t, &s, finished)
	if s.Model == "" {
		// with no model a run is connect only, so its connect result finishes it
		for run := range finished.connected {
			finished.done[run] = true
		}
	}
	if s.Current != nil && (finished.done[s.Current.Run] || t.Sub(s.Current.LastEvent) > stale) {
		s.Current = nil
	}
	s.System = system(c)
	if s.Model != "" {
		up := modelUp(c.ModelURL)
		s.ModelUp = &up
	}
	s.Box, s.Keys = boxKey(c), keysHeld(c.State)
	if c.Root == "" { // not in tests, which have no model server
		s.Models = modelList(c.ModelURL)
	}
	return s
}

type finishedRuns struct {
	done      map[string]bool // runs with their last result written
	connected map[string]bool // runs whose connect result is ok
}

// results reads results.ndjson: today's counts, the latest Recent and which runs are over.
func results(state string, t time.Time, s *Stats) finishedRuns {
	f := finishedRuns{done: map[string]bool{}, connected: map[string]bool{}}
	day := t.Format("2006-01-02")
	var all []journal.Result
	n, _ := journal.Results(state, 0, func(_ int, _ []byte, r journal.Result) {
		if r.Task == "explore" || r.Status != "ok" {
			f.done[r.Run] = true
		} else {
			f.connected[r.Run] = true
		}
		if r.Ended.UTC().Format("2006-01-02") == day {
			if r.Task == "explore" {
				s.Today.Outcomes[word(r.Outcome, outcomes)]++
				if r.Status == "auth" {
					s.Today.Keyed++
				}
			} else {
				s.Today.Statuses[word(r.Status, statuses)]++
			}
		}
		all = append(all, r)
		if len(all) > 4*Recent {
			all = append(all[:0], all[len(all)-Recent:]...)
		}
	})
	s.Results = n
	if len(all) > Recent {
		all = all[len(all)-Recent:]
	}
	for i := len(all) - 1; i >= 0; i-- {
		r := all[i]
		if !slugOK.MatchString(r.Slug) || !runOK.MatchString(r.Run) {
			continue
		}
		out := Result{Run: r.Run, Slug: r.Slug, Task: word(r.Task, tasks), Ended: r.Ended.UTC(), Status: word(r.Status, statuses), Outcome: word(r.Outcome, outcomes),
			Tools: r.Tools, ToolsTried: r.ToolsTried, ToolsOK: r.ToolsOK, Calls: r.Calls, Errors: r.Errors, P50Ms: r.P50Ms, P95Ms: r.P95Ms, WallMs: r.WallMs}
		if modelOK.MatchString(r.Model) {
			out.Model = r.Model
		}
		s.Recent = append(s.Recent, out)
	}
	return f
}

// event is the part of an event line that's read here. "why" and every other free-text field is
// never decoded.
type event struct {
	T            time.Time `json:"t"`
	Run          string    `json:"run"`
	Ev           string    `json:"ev"`
	Slug         string    `json:"slug"`
	Status       string    `json:"status"`
	Turn         int       `json:"turn"`
	Ms           int64     `json:"ms"`
	PromptTokens int64     `json:"promptTokens"`
	OutputTokens int64     `json:"outputTokens"`
	Built        *bool     `json:"built"`
	Model        string    `json:"model"`
}

// events reads today's events file: the counts, the harness's start and the run in progress.
func events(state string, t time.Time, s *Stats, finished finishedRuns) {
	f, err := os.Open(filepath.Join(state, "events", t.Format("2006-01-02")+".ndjson"))
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64<<10), 4<<20)
	var cur *Current
	for sc.Scan() {
		var e event
		if json.Unmarshal(sc.Bytes(), &e) != nil || e.T.IsZero() {
			continue
		}
		s.LastEvent = e.T.UTC()
		switch e.Ev {
		case "harness.start":
			s.Started = e.T.UTC()
			cur = nil
			s.Benching, s.Evaluating = "", ""
		case "eval.start":
			if modelOK.MatchString(e.Model) {
				s.Evaluating = e.Model
			}
		case "eval.end", "eval.failed":
			s.Evaluating = ""
		case "bench.start":
			if modelOK.MatchString(e.Model) {
				s.Benching = e.Model
			}
		case "bench.end", "bench.failed":
			s.Benching = ""
		case "run.start":
			s.Today.Runs++
			cur = nil
			if runOK.MatchString(e.Run) && slugOK.MatchString(e.Slug) {
				cur = &Current{Run: e.Run, Slug: e.Slug, Started: e.T.UTC(), Stage: "connecting"}
			}
		case "connect.ok":
			s.Today.Connected++
			if cur != nil && cur.Run == e.Run {
				cur.Stage = "exploring"
			}
		case "connect.failed":
			s.Today.ConnectFails++
		case "call":
			s.Today.Calls++
			if e.Status != "ok" {
				s.Today.CallErrors++
			}
			if cur != nil && cur.Run == e.Run {
				cur.Calls++
				if e.Status != "ok" {
					cur.Errors++
				}
			}
		case "model.turn":
			s.Today.Turns++
			s.Today.ModelMs += max(e.Ms, 0)
			s.Today.PromptTokens += max(e.PromptTokens, 0)
			s.Today.OutputTokens += max(e.OutputTokens, 0)
			if cur != nil && cur.Run == e.Run {
				cur.Turn = e.Turn
			}
		case "model.failed":
			s.Today.ModelFails++
		case "program":
			s.Today.Programs++
			if e.Built != nil && !*e.Built {
				s.Today.BuildFails++
			}
		case "cell.failed":
			s.Today.CellFails++
		}
		if cur != nil && cur.Run == e.Run {
			cur.LastEvent = e.T.UTC()
		}
	}
	s.Current = cur
}

// system reads the machine from /proc and /sys. A file that isn't there leaves its field empty.
func system(c Config) System {
	var y System
	root := c.Root
	if b, err := os.ReadFile(filepath.Join(root, "/proc/uptime")); err == nil {
		if f := strings.Fields(string(b)); len(f) > 0 {
			v, _ := strconv.ParseFloat(f[0], 64)
			y.UptimeS = int64(v)
		}
	}
	if b, err := os.ReadFile(filepath.Join(root, "/proc/loadavg")); err == nil {
		f := strings.Fields(string(b))
		for i := 0; i < 3 && i < len(f); i++ {
			y.Load[i], _ = strconv.ParseFloat(f[i], 64)
		}
	}
	if b, err := os.ReadFile(filepath.Join(root, "/proc/cpuinfo")); err == nil {
		y.CPUs = strings.Count(string(b), "\nprocessor\t") + map[bool]int{true: 1}[strings.HasPrefix(string(b), "processor\t")]
	}
	if b, err := os.ReadFile(filepath.Join(root, "/proc/meminfo")); err == nil {
		m := map[string]int64{}
		for _, l := range strings.Split(string(b), "\n") {
			f := strings.Fields(l)
			if len(f) >= 2 {
				v, _ := strconv.ParseInt(f[1], 10, 64)
				m[strings.TrimSuffix(f[0], ":")] = v * 1024
			}
		}
		y.MemTotal, y.MemAvail, y.SwapUsed = m["MemTotal"], m["MemAvailable"], m["SwapTotal"]-m["SwapFree"]
	}
	seen := map[uint64]bool{}
	for _, p := range c.Disks {
		var st syscall.Statfs_t
		if p == "" || syscall.Statfs(filepath.Join(root, p), &st) != nil {
			continue
		}
		var fi syscall.Stat_t
		if syscall.Stat(filepath.Join(root, p), &fi) == nil {
			if seen[fi.Dev] {
				continue
			}
			seen[fi.Dev] = true
		}
		y.Disks = append(y.Disks, Disk{Path: p, Total: int64(st.Blocks) * st.Bsize, Free: int64(st.Bavail) * st.Bsize})
	}
	y.GPU = gpu(root)
	y.CPUTempC = hwmon(root, "k10temp", "temp1_input") / 1000
	return y
}

func readInt(p string) (int64, bool) {
	b, err := os.ReadFile(p)
	if err != nil {
		return 0, false
	}
	v, err := strconv.ParseInt(strings.TrimSpace(string(b)), 10, 64)
	return v, err == nil
}

func gpu(root string) *GPU {
	cards, _ := filepath.Glob(filepath.Join(root, "/sys/class/drm/card[0-9]*/device/gpu_busy_percent"))
	sort.Strings(cards)
	for _, busy := range cards {
		dev := filepath.Dir(busy)
		b, ok := readInt(busy)
		if !ok {
			continue
		}
		g := &GPU{BusyPct: int(b)}
		g.VRAMUsed, _ = readInt(filepath.Join(dev, "mem_info_vram_used"))
		g.VRAMTotal, _ = readInt(filepath.Join(dev, "mem_info_vram_total"))
		g.GTTUsed, _ = readInt(filepath.Join(dev, "mem_info_gtt_used"))
		g.GTTTotal, _ = readInt(filepath.Join(dev, "mem_info_gtt_total"))
		mons, _ := filepath.Glob(filepath.Join(dev, "hwmon", "hwmon*"))
		for _, m := range mons {
			if v, ok := readInt(filepath.Join(m, "temp1_input")); ok {
				g.TempC = float64(v) / 1000
			}
			if v, ok := readInt(filepath.Join(m, "power1_average")); ok {
				g.PowerW = float64(v) / 1e6
			} else if v, ok := readInt(filepath.Join(m, "power1_input")); ok {
				g.PowerW = float64(v) / 1e6
			}
		}
		return g
	}
	return nil
}

// hwmon is one value of the hwmon device with this name, 0 when there's none.
func hwmon(root, name, file string) float64 {
	mons, _ := filepath.Glob(filepath.Join(root, "/sys/class/hwmon/hwmon*"))
	for _, m := range mons {
		b, err := os.ReadFile(filepath.Join(m, "name"))
		if err != nil || strings.TrimSpace(string(b)) != name {
			continue
		}
		if v, ok := readInt(filepath.Join(m, file)); ok {
			return float64(v)
		}
	}
	return 0
}

// Box is the box's public key and its fingerprint.
type Box struct {
	Key         string `json:"key"` // uncompressed P-256, base64url
	Fingerprint string `json:"fingerprint"`
}

// KeyHeld is one sealed key in the inbox.
type KeyHeld struct {
	Slug  string    `json:"slug"`
	Box   string    `json:"box"` // the fingerprint it was sealed to
	Added time.Time `json:"added"`
}

func boxKey(c Config) *Box {
	p := c.BoxPub
	if p == "" {
		p = filepath.Join(c.State, "box.pub")
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil
	}
	k, err := seal.ParsePublicKey(string(b))
	if err != nil {
		return nil
	}
	return &Box{Key: seal.PublicKey(k), Fingerprint: seal.Fingerprint(k)}
}

func keysHeld(state string) []KeyHeld {
	out := []KeyHeld{}
	paths, _ := filepath.Glob(filepath.Join(state, "inbox", "keys", "*.json"))
	for _, p := range paths {
		slug := strings.TrimSuffix(filepath.Base(p), ".json")
		fi, err := os.Stat(p)
		b, err2 := os.ReadFile(p)
		var x seal.Sealed
		if err != nil || err2 != nil || !slugOK.MatchString(slug) || json.Unmarshal(b, &x) != nil || x.Slug != slug || x.Check() != nil {
			continue
		}
		out = append(out, KeyHeld{Slug: slug, Box: x.Box, Added: fi.ModTime().UTC()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Slug < out[j].Slug })
	return out
}

// modelList is the models the model server offers, on this machine only; nil when it can't say.
func modelList(base string) []string {
	if base == "" {
		base = "http://127.0.0.1:8080/v1"
	}
	if !local(base) {
		return nil
	}
	cl := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := cl.Get(strings.TrimSuffix(base, "/") + "/models")
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if resp.StatusCode != 200 || json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&list) != nil {
		return nil
	}
	var out []string
	for _, m := range list.Data {
		if modelOK.MatchString(m.ID) && len(out) < 64 {
			out = append(out, m.ID)
		}
	}
	sort.Strings(out)
	return out
}

// local says whether an address is on this machine.
func local(u string) bool {
	host := u
	if i := strings.Index(host, "://"); i >= 0 {
		host = host[i+3:]
	}
	if i := strings.IndexAny(host, "/"); i >= 0 {
		host = host[:i]
	}
	h, _, err := net.SplitHostPort(host)
	if err != nil {
		h = host
	}
	ip := net.ParseIP(h)
	return h == "localhost" || (ip != nil && ip.IsLoopback())
}

// modelUp asks the model's server for /health, on this machine only.
func modelUp(base string) bool {
	if base == "" {
		base = "http://127.0.0.1:8080/v1"
	}
	u := strings.TrimSuffix(strings.TrimSuffix(base, "/"), "/v1") + "/health"
	host := u
	if i := strings.Index(host, "://"); i >= 0 {
		host = host[i+3:]
	}
	if i := strings.IndexAny(host, "/"); i >= 0 {
		host = host[:i]
	}
	h, _, err := net.SplitHostPort(host)
	if err != nil {
		h = host
	}
	if ip := net.ParseIP(h); h != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return false
	}
	cl := &http.Client{Timeout: 2 * time.Second, Transport: &http.Transport{Proxy: nil}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := cl.Get(u)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// Server answers GET /v1/stats, to the addresses in Allow only, with Collect at most every
// Fresh.
type Server struct {
	Config Config
	Allow  []net.IP
	Fresh  time.Duration
	// Secret signs the site's commands (control.go); shorter than 32 bytes, commands are off
	Secret []byte

	mu      sync.Mutex
	last    []byte
	at      time.Time
	replays seen
}

// boxFingerprint is the fingerprint of the box's public key, "" when install.sh hasn't made it.
func (sv *Server) boxFingerprint() string {
	if b := boxKey(sv.Config); b != nil {
		return b.Fingerprint
	}
	return ""
}

func (sv *Server) allowed(remote string) bool {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	for _, a := range sv.Allow {
		if ip != nil && a.Equal(ip) {
			return true
		}
	}
	return false
}

func (sv *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !sv.allowed(r.RemoteAddr) {
		http.Error(w, "no", http.StatusForbidden)
		return
	}
	if r.URL.Path == "/v1/command" {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			http.Error(w, "POST", http.StatusMethodNotAllowed)
			return
		}
		sv.command(w, r)
		return
	}
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", "GET")
		http.Error(w, "GET", http.StatusMethodNotAllowed)
		return
	}
	switch r.URL.Path {
	case "/v1/stats":
	case "/v1/results":
		sv.results(w, r)
		return
	case "/v1/run":
		sv.transcript(w, r)
		return
	case "/v1/evals":
		sv.evals(w)
		return
	default:
		http.NotFound(w, r)
		return
	}
	sv.mu.Lock()
	if sv.last == nil || time.Since(sv.at) >= sv.Fresh {
		b, err := json.Marshal(Collect(sv.Config))
		if err != nil {
			sv.mu.Unlock()
			http.Error(w, "stats", http.StatusInternalServerError)
			return
		}
		sv.last, sv.at = b, time.Now()
	}
	b := sv.last
	sv.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	sv.signAnswer(w, "/v1/stats", b)
	w.Write(b)
}

// ResultsPage is the most result lines one answer of /v1/results carries.
const ResultsPage = 500

// results answers GET /v1/results?after=N with the result lines after line N, in the export format
// (journal.Export): the lines as the harness wrote them, which are already only numbers and fixed
// words, and which the site checks one field at a time all the same. The site keeps N and asks
// for what came after it.
func (sv *Server) results(w http.ResponseWriter, r *http.Request) {
	after, err := strconv.Atoi(r.URL.Query().Get("after"))
	if err != nil || after < 0 {
		http.Error(w, "after: a line number", http.StatusBadRequest)
		return
	}
	var buf bytes.Buffer
	if err := journal.Export(sv.Config.State, after, ResultsPage, &buf); err != nil {
		http.Error(w, "results", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	sv.signAnswer(w, "/v1/results", buf.Bytes())
	w.Write(buf.Bytes())
}

// Listen checks the address it's given: a real address of this machine, never every interface,
// because the box's stats are for one server on the local network.
func Listen(addr string) (net.Listener, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	ip := net.ParseIP(host)
	if ip == nil || ip.IsUnspecified() {
		return nil, &net.AddrError{Err: "give the box's own address on the local network, not a name or every interface", Addr: addr}
	}
	return net.Listen("tcp", addr)
}
