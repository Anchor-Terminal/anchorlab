package seal

import (
	"encoding/base64"
	"path/filepath"
	"strings"
	"testing"
)

func TestSealAndOpen(t *testing.T) {
	k, err := NewKey()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := WriteKey(k, filepath.Join(dir, "box.key"), filepath.Join(dir, "box.pub")); err != nil {
		t.Fatal(err)
	}
	if err := WriteKey(k, filepath.Join(dir, "box.key"), filepath.Join(dir, "box.pub")); err == nil {
		t.Error("replaced a key that was there")
	}
	k2, err := LoadKey(filepath.Join(dir, "box.key"))
	if err != nil || Fingerprint(k2.PublicKey()) != Fingerprint(k.PublicKey()) {
		t.Fatalf("loaded back: %v", err)
	}
	sec := Secret{Kind: "header", Name: "Authorization", Prefix: "Bearer ", Value: "sk-test-a9F2"}
	x, err := Seal(k.PublicKey(), "zenrows", "https://MCP.zenrows.com/mcp?x=1", sec)
	if err != nil {
		t.Fatal(err)
	}
	if x.Origin != "https://mcp.zenrows.com" || x.Check() != nil || strings.Contains(x.CT, "sk-test") {
		t.Fatalf("sealed: %+v", x)
	}
	got, err := Open(k2, x, "zenrows", "https://mcp.zenrows.com/other/path")
	if err != nil || got != sec {
		t.Fatalf("opened: %+v %v", got, err)
	}
	other, _ := NewKey()
	for name, try := range map[string]func() error{
		"another listing": func() error { _, err := Open(k, x, "kiwi", "https://mcp.zenrows.com/mcp"); return err },
		"another server":  func() error { _, err := Open(k, x, "zenrows", "https://evil.example/mcp"); return err },
		"another box":     func() error { _, err := Open(other, x, "zenrows", "https://mcp.zenrows.com/mcp"); return err },
		"the origin edited to match": func() error {
			y := x
			y.Origin, y.Slug = "https://evil.example", "zenrows"
			_, err := Open(k, y, "zenrows", "https://evil.example/mcp")
			return err
		},
		"a byte changed": func() error {
			y := x
			ct, _ := base64.RawURLEncoding.DecodeString(y.CT)
			ct[0] ^= 1
			y.CT = base64.RawURLEncoding.EncodeToString(ct)
			_, err := Open(k, y, "zenrows", "https://mcp.zenrows.com/mcp")
			return err
		},
		"plain http": func() error { _, err := Open(k, x, "zenrows", "http://mcp.zenrows.com/mcp"); return err },
	} {
		if try() == nil {
			t.Errorf("opened for %s", name)
		}
	}
}

func TestSecretCheck(t *testing.T) {
	for _, s := range []Secret{
		{Kind: "header", Name: "Host", Value: "x"},
		{Kind: "header", Name: "Cookie", Value: "x"},
		{Kind: "header", Name: "X-Api-Key", Value: "a\r\nInjected: 1"},
		{Kind: "header", Name: "X Api", Value: "x"},
		{Kind: "query", Name: "api key", Value: "x"},
		{Kind: "oauth-client", TokenURL: "http://auth.example/token", ClientID: "a", ClientSecret: "b"},
		{Kind: "ssh", Value: "x"},
	} {
		if s.Check() == nil {
			t.Errorf("took %+v", s)
		}
	}
	for _, s := range []Secret{
		{Kind: "header", Name: "X-Api-Key", Value: "abc"},
		{Kind: "query", Name: "api_key", Value: "abc"},
		{Kind: "oauth-client", TokenURL: "https://auth.example/oauth/token", ClientID: "a", ClientSecret: "b", Scope: "read"},
	} {
		if err := s.Check(); err != nil {
			t.Errorf("refused %+v: %v", s, err)
		}
	}
}
