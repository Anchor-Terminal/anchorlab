package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const petsSpec = `{
 "openapi": "3.0.3",
 "info": {"title": "Pets"},
 "servers": [{"url": "/v1"}],
 "components": {"parameters": {"petId": {"name": "petId", "in": "path", "required": true, "schema": {"type": "string"}}}},
 "paths": {
  "/pets": {
   "get": {"operationId": "listPets", "summary": "List pets", "parameters": [
     {"name": "limit", "in": "query", "schema": {"type": "integer"}},
     {"name": "tag", "in": "query", "schema": {"type": "array", "items": {"type": "string"}}},
     {"name": "X-Trace", "in": "header", "schema": {"type": "string"}}]},
   "post": {"operationId": "createPet", "summary": "Add a pet"}
  },
  "/pets/{petId}": {
   "parameters": [{"$ref": "#/components/parameters/petId"}],
   "get": {"summary": "One pet"},
   "delete": {"operationId": "delete pet!", "summary": "Remove a pet"}
  },
  "/old": {"get": {"operationId": "oldThing", "deprecated": true}},
  "/secret": {"get": {"operationId": "secret"}},
  "/boom": {"get": {"operationId": "boom"}},
  "/big": {"get": {"operationId": "big"}}
 }
}`

// a made-up API with its description at /openapi.json, and the requests it was sent
func petsAPI(t *testing.T, got *[]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/openapi.json" {
			*got = append(*got, r.Method+" "+r.URL.RequestURI())
		}
		switch {
		case r.URL.Path == "/openapi.json":
			w.Write([]byte(petsSpec))
		case r.Method != "GET":
			t.Errorf("the lab sent a %s", r.Method)
		case r.URL.Path == "/v1/pets":
			fmt.Fprintf(w, `{"pets":[{"id":"rex"}],"limit":%q}`, r.URL.Query().Get("limit"))
		case r.URL.Path == "/v1/pets/rex" || r.URL.EscapedPath() == "/v1/pets/a%20b":
			w.Write([]byte(`{"id":"rex"}`))
		case strings.HasPrefix(r.URL.Path, "/v1/pets/"):
			http.Error(w, `{"error":"no pet with that id"}`, http.StatusNotFound)
		case r.URL.Path == "/v1/secret":
			http.Error(w, "who are you", http.StatusUnauthorized)
		case r.URL.Path == "/v1/boom":
			http.Error(w, "it fell over", http.StatusInternalServerError)
		case r.URL.Path == "/v1/big":
			w.Write([]byte(strings.Repeat("x", 3000)))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// An HTTP API's operations are its tools: the GET ones callable with their path and query
// parameters as arguments, the rest listed and never sent.
func TestOpenAPI(t *testing.T) {
	defer AllowLoopbackForTests()()
	var got []string
	srv := petsAPI(t, &got)
	lim := DefaultLimits
	lim.MinInterval, lim.MaxText = 0, 1000
	s, tools, err := OpenAPI(context.Background(), srv.URL+"/openapi.json", "", "anchor-lab/test", lim)
	if err != nil {
		t.Fatal(err)
	}
	if !s.IsAPI() || s.URL != srv.URL+"/v1" || s.Protocol != "openapi/3.0.3" || s.Server != "Pets" {
		t.Errorf("the session: %s %s %s", s.URL, s.Protocol, s.Server)
	}
	if all, reads, offered := s.Operations(); all != 8 || reads != 6 || offered != 5 {
		t.Errorf("operations: %d in all, %d GET, %d offered", all, reads, offered)
	}
	by := map[string]Tool{}
	var names []string
	for _, tool := range tools {
		by[tool.Name] = tool
		names = append(names, tool.Name)
	}
	if strings.Join(names, " ") != "big boom listPets createPet get_pets_petId delete_pets_petId secret" {
		t.Errorf("the tools: %v", names)
	}
	if _, listed := by["oldThing"]; listed {
		t.Error("a deprecated operation was offered")
	}
	for name, callable := range map[string]bool{"listPets": true, "get_pets_petId": true, "createPet": false, "delete_pets_petId": false} {
		tool, ok := by[name]
		if !ok {
			t.Fatalf("no tool %s in %v", name, names)
		}
		if c, _ := Callable(tool); c != callable {
			t.Errorf("%s callable: %v", name, c)
		}
	}
	if d := by["listPets"].Description; !strings.HasPrefix(d, "GET /pets. List pets") {
		t.Errorf("a description: %q", d)
	}
	var schema struct {
		Properties map[string]map[string]any `json:"properties"`
		Required   []string                  `json:"required"`
	}
	json.Unmarshal(by["get_pets_petId"].InputSchema, &schema)
	if len(schema.Required) != 1 || schema.Required[0] != "petId" || schema.Properties["petId"]["type"] != "string" {
		t.Errorf("a path parameter from a $ref: %s", by["get_pets_petId"].InputSchema)
	}
	schema.Properties = nil
	json.Unmarshal(by["listPets"].InputSchema, &schema)
	if _, header := schema.Properties["X-Trace"]; header || len(schema.Properties) != 2 {
		t.Errorf("only path and query parameters are arguments: %s", by["listPets"].InputSchema)
	}

	ctx := context.Background()
	call := func(name, args string) Reply { return s.CallTool(ctx, name, json.RawMessage(args)) }
	if r := call("listPets", `{"limit": 2, "tag": ["a", "b c"]}`); !r.OK || !strings.Contains(string(r.Data), `"limit":"2"`) {
		t.Errorf("listPets: %+v", r)
	}
	if r := call("get_pets_petId", `{"petId": "a b"}`); !r.OK || r.Text != `{"id":"rex"}` {
		t.Errorf("a path parameter: %+v", r)
	}
	if strings.Join(got, " | ") != "GET /v1/pets?limit=2&tag=a&tag=b+c | GET /v1/pets/a%20b" {
		t.Errorf("what the API was sent: %v", got)
	}
	// calls the lab can't build aren't sent, and say why
	sent := len(got)
	for args, want := range map[string]string{`{}`: `"petId" is required`, `{"petId": "rex", "colour": "red"}`: `no parameter "colour"; it takes: petId`, `{"petId": {"a": 1}}`: "has to be a string"} {
		if r := call("get_pets_petId", args); r.OK || !strings.Contains(r.Error, want) {
			t.Errorf("%s: %+v", args, r)
		}
	}
	if r := call("createPet", `{}`); r.OK || !strings.Contains(r.Error, "isn't a GET") {
		t.Errorf("a POST: %+v", r)
	}
	if len(got) != sent {
		t.Errorf("a call that couldn't be built was sent: %v", got[sent:])
	}
	// the API's own answers: a 404 in its words, a 401, a 500, and an answer too long to hand over whole
	if r := call("get_pets_petId", `{"petId": "nobody"}`); r.OK || !strings.Contains(r.Error, "HTTP 404") || !strings.Contains(r.Error, "no pet with that id") {
		t.Errorf("a 404: %+v", r)
	}
	if r := call("secret", `{}`); r.OK || !strings.Contains(r.Error, "HTTP 401") {
		t.Errorf("a 401: %+v", r)
	}
	if r := call("boom", `{}`); r.OK || !strings.Contains(r.Error, "HTTP 500") {
		t.Errorf("a 500: %+v", r)
	}
	if r := call("big", `{}`); !r.OK || len(r.Text) > 1100 || r.Data != nil {
		t.Errorf("a long answer: %d characters", len(r.Text))
	}
	var statuses []string
	cut := false
	for _, c := range s.Calls() {
		statuses = append(statuses, c.Status)
		cut = cut || c.Cut
		if c.Tool == "" && c.Status != "refused" {
			t.Errorf("a call with no tool: %+v", c)
		}
	}
	if strings.Join(statuses, " ") != "ok ok tool_error tool_error tool_error refused tool_error auth http_error ok" || !cut {
		t.Errorf("the calls: %v, cut %v", statuses, cut)
	}
}

// A description the lab can't use says so, and the API's address can be given.
func TestOpenAPIRefuses(t *testing.T) {
	defer AllowLoopbackForTests()()
	answer := "openapi: 3.0.3\npaths: {}\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/gone.json" {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(answer))
	}))
	defer srv.Close()
	lim := DefaultLimits
	lim.MinInterval = 0
	open := func(spec, base string) (*Session, error) {
		s, _, err := OpenAPI(context.Background(), spec, base, "anchor-lab/test", lim)
		return s, err
	}
	status := func(err error) string {
		var es *ErrStatus
		if errors.As(err, &es) {
			return es.Status
		}
		return ""
	}
	if _, err := open(srv.URL+"/openapi.yaml", ""); status(err) != "error" || !strings.Contains(err.Error(), "paths") {
		t.Errorf("a description with no paths: %v", err)
	}
	answer = "openapi: 3.0.3\npaths:\n\t/a: {}\n"
	if _, err := open(srv.URL+"/openapi.yaml", ""); status(err) != "error" || !strings.Contains(err.Error(), "neither JSON nor YAML") {
		t.Errorf("a description that's broken YAML: %v", err)
	}
	answer = "<html><body>Not found</body></html>"
	if _, err := open(srv.URL+"/openapi.yaml", ""); status(err) != "error" {
		t.Errorf("a page that isn't a description: %v", err)
	}
	if _, err := open(srv.URL+"/gone.json", ""); status(err) != "error" {
		t.Errorf("a description that isn't there: %v", err)
	}
	if _, err := open("http://10.0.0.1/openapi.json", ""); status(err) != "refused" {
		t.Errorf("a description at a private address: %v", err)
	}
	answer = `{"openapi":"3.1.0","paths":{"/a":{"post":{}}}}`
	if _, err := open(srv.URL+"/openapi.json", ""); status(err) != "error" || !strings.Contains(err.Error(), "no GET") {
		t.Errorf("a description with nothing to read: %v", err)
	}
	answer = `{"openapi":"3.1.0","servers":[{"url":"https://{region}.example.com"}],"paths":{"/a":{"get":{}}}}`
	if _, err := open(srv.URL+"/openapi.json", ""); status(err) != "error" || !strings.Contains(err.Error(), "variables") {
		t.Errorf("a server address with variables: %v", err)
	}
	// a description past what one may weigh, which is its own limit and not an answer's
	answer = `{"openapi":"3.1.0","paths":{"/a":{"get":{}}},"x-padding":"` + strings.Repeat("x", int(lim.MaxBody)) + `"}`
	if _, err := open(srv.URL+"/openapi.json", ""); err != nil {
		t.Errorf("a description bigger than an answer may be: %v", err)
	}
	answer = "openapi: 3.1.0\n" + strings.Repeat("#", MaxSpec)
	if _, err := open(srv.URL+"/openapi.yaml", ""); status(err) != "error" || !strings.Contains(err.Error(), "over") {
		t.Errorf("a description over the limit: %v", err)
	}
	// the API's address, given: used as it is
	answer = `{"openapi":"3.1.0","servers":[{"url":"https://{region}.example.com"}],"paths":{"/a":{"get":{}}}}`
	if s, err := open(srv.URL+"/openapi.json", srv.URL+"/api/"); err != nil || s.URL != srv.URL+"/api" {
		t.Errorf("a given address: %v", err)
	}
	// Swagger 2 keeps its address in host and basePath
	answer = `{"swagger":"2.0","host":"` + strings.TrimPrefix(srv.URL, "http://") + `","basePath":"/v2","schemes":["http"],"paths":{"/a":{"get":{"parameters":[{"name":"q","in":"query","type":"integer"}]}}}}`
	s, tools, err := OpenAPI(context.Background(), srv.URL+"/swagger.json", "", "anchor-lab/test", lim)
	if err != nil || s.URL != srv.URL+"/v2" || len(tools) != 1 || tools[0].Name != "get_a" || !strings.Contains(string(tools[0].InputSchema), `"integer"`) {
		t.Errorf("Swagger 2: %v %+v", err, tools)
	}
	// JSON behind a byte order mark, and YAML written in braces, both read
	for name, body := range map[string]string{
		"JSON with a byte order mark": "\xef\xbb\xbf" + `{"openapi":"3.1.0","paths":{"/a":{"get":{}}}}`,
		"YAML in braces":              `{openapi: 3.1.0, paths: {/a: {get: {}}}}`,
	} {
		answer = body
		if _, tools, err := OpenAPI(context.Background(), srv.URL+"/openapi", srv.URL, "anchor-lab/test", lim); err != nil || len(tools) != 1 || tools[0].Name != "get_a" {
			t.Errorf("%s: %v %+v", name, err, tools)
		}
	}
	// and in YAML its version is often written bare, which makes it a number and not "2.0"
	answer = "swagger: 2.0\nhost: " + strings.TrimPrefix(srv.URL, "http://") + "\nbasePath: /v2\nschemes: [http]\npaths:\n  /a:\n    get:\n      parameters:\n      - name: q\n        in: query\n        type: integer\n"
	s, tools, err = OpenAPI(context.Background(), srv.URL+"/swagger.yaml", "", "anchor-lab/test", lim)
	if err != nil || s.Protocol != "openapi/2.0" || s.URL != srv.URL+"/v2" || len(tools) != 1 || tools[0].Name != "get_a" || !strings.Contains(string(tools[0].InputSchema), `"integer"`) {
		t.Errorf("Swagger 2 in YAML: %v %+v %+v", err, s, tools)
	}
}

// The same API described in YAML, the way people write it by hand, opens as the same tools.
func TestOpenAPIInYAML(t *testing.T) {
	defer AllowLoopbackForTests()()
	const inYAML = `# the pets API
openapi: 3.1
info:
  title: Pets
servers:
- url: /v1
components:
  parameters:
    petId: &petId
      name: petId
      in: path
      required: true
      schema: {type: string}
paths:
  /pets:
    get:
      operationId: listPets
      summary: List pets   # all of them
      description: |
        A long description
        over two lines.
      parameters:
        - name: limit
          in: query
          schema:
            type: integer
        - name: tag
          in: query
          schema: {type: array, items: {type: string}}
        - {name: X-Trace, in: header, schema: {type: string}}
    post:
      operationId: createPet
      summary: Add a pet
  /pets/{petId}:
    parameters:
      - $ref: '#/components/parameters/petId'
    get:
      summary: >-
        One
        pet
    delete:
      operationId: "delete pet!"
      summary: Remove a pet
  /old: {get: {operationId: oldThing, deprecated: true}}
  /secret:
    get: {operationId: secret}
  /boom:
    get:
      operationId: boom
  /big:
    get:
      operationId: big
  x-internal: not a path
`
	var got []string
	api := petsAPI(t, &got)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/yaml")
		w.Write([]byte(inYAML))
	}))
	defer srv.Close()
	lim := DefaultLimits
	lim.MinInterval = 0
	ctx := context.Background()
	fromJSON, wantTools, err := OpenAPI(ctx, api.URL+"/openapi.json", "", "anchor-lab/test", lim)
	if err != nil {
		t.Fatal(err)
	}
	s, tools, err := OpenAPI(ctx, srv.URL+"/openapi.yaml", api.URL+"/v1", "anchor-lab/test", lim)
	if err != nil {
		t.Fatal(err)
	}
	if s.Protocol != "openapi/3.1" || s.Server != "Pets" || s.URL != fromJSON.URL || len(tools) != len(wantTools) {
		t.Fatalf("the session: %s %s %s, %d tools of %d", s.Protocol, s.Server, s.URL, len(tools), len(wantTools))
	}
	for i, want := range wantTools {
		got := tools[i]
		if got.Name != want.Name || string(got.InputSchema) != string(want.InputSchema) {
			t.Errorf("tool %d: %s %s, want %s %s", i, got.Name, got.InputSchema, want.Name, want.InputSchema)
		}
		if a, b := Callable(got); a != func() bool { ok, _ := Callable(want); return ok }() {
			t.Errorf("%s callable: %v %s", got.Name, a, b)
		}
	}
	if d := tools[2].Description; d != "GET /pets. List pets" {
		t.Errorf("a summary with a comment after it: %q", d)
	}
	if d := tools[4].Description; d != "GET /pets/{petId}. One pet" {
		t.Errorf("a folded summary: %q", d)
	}
	if r := s.CallTool(ctx, "get_pets_petId", json.RawMessage(`{"petId": "rex"}`)); !r.OK || len(got) != 1 || got[0] != "GET /v1/pets/rex" {
		t.Errorf("a call: %+v, sent %v", r, got)
	}
}

// Real descriptions, for whoever has some: with ANCHOR_LAB_YAML_DIR set to a folder, every .yaml
// or .yml file in it that has a .json file of the same name beside it has to open as the same API
// either way: the same address, the same operations with the same arguments, or the same refusal.
func TestRealDescriptions(t *testing.T) {
	dir := os.Getenv("ANCHOR_LAB_YAML_DIR")
	if dir == "" {
		t.Skip("ANCHOR_LAB_YAML_DIR isn't set")
	}
	defer AllowLoopbackForTests()()
	srv := httptest.NewServer(http.FileServer(http.Dir(dir)))
	defer srv.Close()
	lim := DefaultLimits
	lim.MinInterval = 0
	files, _ := filepath.Glob(filepath.Join(dir, "*.y*ml"))
	opened, refused, operations := 0, 0, 0
	why := map[string]int{}
	for _, file := range files {
		name := filepath.Base(file)
		twin := strings.TrimSuffix(name, filepath.Ext(name)) + ".json"
		if _, err := os.Stat(filepath.Join(dir, twin)); err != nil {
			continue
		}
		want, wantTools, wantErr := OpenAPI(context.Background(), srv.URL+"/"+url.PathEscape(twin), "", "anchor-lab/test", lim)
		got, gotTools, err := OpenAPI(context.Background(), srv.URL+"/"+url.PathEscape(name), "", "anchor-lab/test", lim)
		if wantErr != nil || err != nil {
			if wantErr == nil || err == nil || wantErr.Error() != err.Error() {
				t.Errorf("%s: %v in YAML and %v in JSON", name, err, wantErr)
			}
			refused++
			var es *ErrStatus
			if errors.As(err, &es) {
				detail, _, _ := strings.Cut(es.Detail, ": ")
				why[es.Status+", "+clip(detail, 70)]++
			}
			continue
		}
		opened++
		operations += len(gotTools)
		if got.Protocol != want.Protocol || got.Server != want.Server || got.URL != want.URL || len(gotTools) != len(wantTools) {
			t.Errorf("%s: %s %q %s with %d operations in YAML, %s %q %s with %d in JSON", name, got.Protocol, got.Server, got.URL, len(gotTools), want.Protocol, want.Server, want.URL, len(wantTools))
			continue
		}
		for i, w := range wantTools {
			g := gotTools[i]
			var gs, ws any
			json.Unmarshal(g.InputSchema, &gs)
			json.Unmarshal(w.InputSchema, &ws)
			gOK, _ := Callable(g)
			wOK, _ := Callable(w)
			if g.Name != w.Name || g.Description != w.Description || gOK != wOK || !reflect.DeepEqual(gs, ws) {
				t.Errorf("%s: operation %d is %s %q %s in YAML and %s %q %s in JSON", name, i, g.Name, g.Description, g.InputSchema, w.Name, w.Description, w.InputSchema)
				break
			}
		}
	}
	t.Logf("%d descriptions opened the same from YAML as from JSON, with %d operations, and %d were refused the same way", opened, operations, refused)
	for reason, n := range why {
		t.Logf("%d refused: %s", n, reason)
	}
}
