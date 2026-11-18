// Package seal is how a key for a server reaches the lab without anyone else being able to read it
// (DESIGN.md, "Keys only the box can open").
//
// The box has a key pair of its own, made once by install.sh. The private half is a file only root
// can read, handed to the harness's service alone (systemd's LoadCredential), so no other process
// on the box, no person in the anchorlab group and no cell can read it. The public half is given
// out: the site's admin page fetches it, and the admin's browser seals a server's key to it before
// anything is sent. The site holds only sealed copies and can't open them.
//
// A sealed copy is ECDH on P-256 between a key made for that one seal and the box's key, HKDF-SHA256
// over the shared secret, and AES-256-GCM. What's sealed is bound to the listing's slug, the address
// of its server and the box's key: the additional data names all three, so a copy sealed for one
// listing can't be replayed against another server, and one sealed for another box won't open. The
// same steps run in the browser with WebCrypto (the site's site.js), and nothing here is beyond the
// standard library.
package seal

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
)

// Version names the format of a sealed copy and is part of what's bound into it.
const Version = "anchor-lab-sealed/1"

// Sealed is a key for one listing's server, readable by one box.
type Sealed struct {
	V      string `json:"v"`
	Slug   string `json:"slug"`
	Origin string `json:"origin"` // https://host[:port] of the listing's server
	Box    string `json:"box"`    // the fingerprint of the box's public key it was sealed to
	EPK    string `json:"epk"`    // the one-time public key, uncompressed P-256, base64url
	IV     string `json:"iv"`     // 12 bytes, base64url
	CT     string `json:"ct"`     // the sealed Secret with its tag, base64url
}

// Secret is what's sealed: how to send the key, and the key.
type Secret struct {
	// Kind is header (Name: Prefix+Value), query (Name=Value in the address) or oauth-client (a
	// client id and secret swapped at TokenURL for a token, sent as Authorization: Bearer).
	Kind   string `json:"kind"`
	Name   string `json:"name,omitempty"`
	Prefix string `json:"prefix,omitempty"`
	Value  string `json:"value,omitempty"`

	TokenURL     string `json:"tokenUrl,omitempty"`
	ClientID     string `json:"clientId,omitempty"`
	ClientSecret string `json:"clientSecret,omitempty"`
	Scope        string `json:"scope,omitempty"`
}

var (
	slugOK = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,79}$`)
	// a header name a server may want a key in; never one that steers the request itself
	headerOK     = regexp.MustCompile(`^[A-Za-z0-9-]{1,64}$`)
	queryOK      = regexp.MustCompile(`^[A-Za-z0-9_.-]{1,64}$`)
	headerBanned = map[string]bool{"host": true, "content-length": true, "content-type": true, "transfer-encoding": true, "connection": true,
		"cookie": true, "user-agent": true, "accept": true, "mcp-session-id": true, "mcp-protocol-version": true, "te": true, "upgrade": true,
		"proxy-authorization": true, "forwarded": true, "x-forwarded-for": true, "x-forwarded-host": true}
)

// Check says whether a Secret is one the gateway may use.
func (s Secret) Check() error {
	ok := func(v string, max int) bool {
		return v != "" && len(v) <= max && !strings.ContainsAny(v, "\r\n\x00")
	}
	switch s.Kind {
	case "header":
		if !headerOK.MatchString(s.Name) || headerBanned[strings.ToLower(s.Name)] {
			return errors.New("not a header the lab sends a key in")
		}
		if !ok(s.Value, 4096) || len(s.Prefix) > 32 || strings.ContainsAny(s.Prefix, "\r\n\x00") {
			return errors.New("the key or its prefix won't go in a header")
		}
	case "query":
		if !queryOK.MatchString(s.Name) || !ok(s.Value, 4096) {
			return errors.New("not a query parameter the lab sends a key in")
		}
	case "oauth-client":
		u, err := url.Parse(s.TokenURL)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
			return errors.New("the token address has to be https")
		}
		if !ok(s.ClientID, 512) || !ok(s.ClientSecret, 4096) || len(s.Scope) > 512 || strings.ContainsAny(s.Scope, "\r\n\x00") {
			return errors.New("the client id, secret or scope won't do")
		}
	default:
		return errors.New("a key is sent as a header, a query parameter or an OAuth client")
	}
	return nil
}

// Origin is the scheme, host and port of a server's address: what a key is bound to.
func Origin(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
		return "", errors.New("not an https address")
	}
	return "https://" + strings.ToLower(u.Host), nil
}

// NewKey makes the box's key pair.
func NewKey() (*ecdh.PrivateKey, error) { return ecdh.P256().GenerateKey(rand.Reader) }

// WriteKey writes the private key as PKCS #8 PEM, readable by its owner alone, and the public key
// beside wherever pub says, readable by anyone. It won't replace a key that's there.
func WriteKey(k *ecdh.PrivateKey, keyPath, pubPath string) error {
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(keyPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err := pem.Encode(f, &pem.Block{Type: "PRIVATE KEY", Bytes: der}); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.WriteFile(pubPath, []byte(PublicKey(k.PublicKey())+"\n"), 0o644)
}

// LoadKey reads the box's private key.
func LoadKey(path string) (*ecdh.PrivateKey, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	blk, _ := pem.Decode(b)
	if blk == nil || blk.Type != "PRIVATE KEY" {
		return nil, errors.New("not a PEM private key")
	}
	k, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
	if err != nil {
		return nil, err
	}
	switch k := k.(type) {
	case *ecdh.PrivateKey:
		if k.Curve() == ecdh.P256() {
			return k, nil
		}
	default:
		// x509 gives an *ecdsa.PrivateKey for P-256; its ECDH form is the same key
		if e, ok := k.(interface {
			ECDH() (*ecdh.PrivateKey, error)
		}); ok {
			return e.ECDH()
		}
	}
	return nil, errors.New("not a P-256 key")
}

// PublicKey is the public key as it's given out: uncompressed, base64url without padding.
func PublicKey(p *ecdh.PublicKey) string { return base64.RawURLEncoding.EncodeToString(p.Bytes()) }

// ParsePublicKey reads a public key in that form.
func ParsePublicKey(s string) (*ecdh.PublicKey, error) {
	b, err := base64.RawURLEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil {
		return nil, err
	}
	return ecdh.P256().NewPublicKey(b)
}

// Fingerprint names a public key in a few characters: the first 16 hex of its SHA-256.
func Fingerprint(p *ecdh.PublicKey) string {
	h := sha256.Sum256(p.Bytes())
	return hex.EncodeToString(h[:8])
}

func aad(slug, origin, box string) []byte {
	return []byte(Version + "\n" + slug + "\n" + origin + "\n" + box)
}

func aead(shared, epk, boxPub []byte) (cipher.AEAD, error) {
	k, err := hkdf.Key(sha256.New, shared, append(append([]byte{}, epk...), boxPub...), Version, 32)
	if err != nil {
		return nil, err
	}
	b, err := aes.NewCipher(k)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(b)
}

// Seal seals a Secret to a box's public key for one listing's server. The admin's browser does the
// same in WebCrypto; this is for tests and for sealing from a terminal.
func Seal(box *ecdh.PublicKey, slug, serverURL string, s Secret) (Sealed, error) {
	if !slugOK.MatchString(slug) {
		return Sealed{}, errors.New("not a slug")
	}
	origin, err := Origin(serverURL)
	if err != nil {
		return Sealed{}, err
	}
	if err := s.Check(); err != nil {
		return Sealed{}, err
	}
	eph, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		return Sealed{}, err
	}
	shared, err := eph.ECDH(box)
	if err != nil {
		return Sealed{}, err
	}
	g, err := aead(shared, eph.PublicKey().Bytes(), box.Bytes())
	if err != nil {
		return Sealed{}, err
	}
	iv := make([]byte, g.NonceSize())
	rand.Read(iv)
	pt, _ := json.Marshal(s)
	fp := Fingerprint(box)
	enc := base64.RawURLEncoding.EncodeToString
	return Sealed{V: Version, Slug: slug, Origin: origin, Box: fp, EPK: enc(eph.PublicKey().Bytes()), IV: enc(iv), CT: enc(g.Seal(nil, iv, pt, aad(slug, origin, fp)))}, nil
}

// Check says whether a sealed copy is shaped right, without opening it: what the site and the
// box's command endpoint can check, neither holding the private key.
func (x Sealed) Check() error {
	dec := base64.RawURLEncoding.DecodeString
	epk, e1 := dec(x.EPK)
	iv, e2 := dec(x.IV)
	ct, e3 := dec(x.CT)
	switch {
	case x.V != Version:
		return errors.New("not a sealed key this box reads")
	case !slugOK.MatchString(x.Slug):
		return errors.New("not a slug")
	case e1 != nil || e2 != nil || e3 != nil || len(epk) != 65 || len(iv) != 12 || len(ct) < 17 || len(ct) > 16<<10:
		return errors.New("not shaped like a sealed key")
	case len(x.Box) != 16:
		return errors.New("no box named")
	}
	if o, err := Origin(x.Origin); err != nil || o != x.Origin {
		return errors.New("the server's address isn't an https origin")
	}
	return nil
}

// Open opens a sealed copy with the box's private key, for the listing and server it's used on
// now. It refuses a copy sealed for another listing, another server or another box.
func Open(k *ecdh.PrivateKey, x Sealed, slug, serverURL string) (Secret, error) {
	if err := x.Check(); err != nil {
		return Secret{}, err
	}
	origin, err := Origin(serverURL)
	if err != nil {
		return Secret{}, err
	}
	if x.Slug != slug || x.Origin != origin {
		return Secret{}, fmt.Errorf("sealed for %s at another address, not this server", x.Slug)
	}
	if x.Box != Fingerprint(k.PublicKey()) {
		return Secret{}, errors.New("sealed to another box's key")
	}
	dec := base64.RawURLEncoding.DecodeString
	epkB, _ := dec(x.EPK)
	iv, _ := dec(x.IV)
	ct, _ := dec(x.CT)
	epk, err := ecdh.P256().NewPublicKey(epkB)
	if err != nil {
		return Secret{}, errors.New("not a P-256 point")
	}
	shared, err := k.ECDH(epk)
	if err != nil {
		return Secret{}, err
	}
	g, err := aead(shared, epkB, k.PublicKey().Bytes())
	if err != nil {
		return Secret{}, err
	}
	pt, err := g.Open(nil, iv, ct, aad(x.Slug, x.Origin, x.Box))
	if err != nil {
		return Secret{}, errors.New("doesn't open with this box's key")
	}
	var s Secret
	if err := json.Unmarshal(pt, &s); err != nil {
		return Secret{}, errors.New("opened, but isn't a key")
	}
	if err := s.Check(); err != nil {
		return Secret{}, err
	}
	return s, nil
}
