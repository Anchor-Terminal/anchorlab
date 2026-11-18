package main

import (
	"context"
	"crypto/ecdh"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/Anchor-Terminal/anchorlab/gateway"
	"github.com/Anchor-Terminal/anchorlab/journal"
	"github.com/Anchor-Terminal/anchorlab/seal"
	"github.com/Anchor-Terminal/anchorlab/targets"
)

// boxKeyCmd makes the box's key pair, once (package seal). install.sh runs it as root: the private
// half goes where only root reads it and systemd hands it to the harness's service alone, the
// public half where the stats service gives it out.
//
//	anchor-lab boxkey -key /etc/anchor-lab/box.key -pub /etc/anchor-lab/box.pub
func boxKeyCmd(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("boxkey", flag.ExitOnError)
	keyPath := fs.String("key", "/etc/anchor-lab/box.key", "the private half, readable by its owner alone")
	pubPath := fs.String("pub", "/etc/anchor-lab/box.pub", "the public half, for the stats to give out: in root's folder, so only root can change which key the site seals to")
	fs.Parse(args)
	if k, err := seal.LoadKey(*keyPath); err == nil {
		// there already: write the public half again in case it went, and say which key it is
		if err := os.WriteFile(*pubPath, []byte(seal.PublicKey(k.PublicKey())+"\n"), 0o644); err != nil {
			return err
		}
		fmt.Fprintf(out, "the box's key is there already: %s\n", seal.Fingerprint(k.PublicKey()))
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%s is there but doesn't read as a key: %w (move it away to make a new one, and every key sealed to it will need sealing again)", *keyPath, err)
	}
	k, err := seal.NewKey()
	if err != nil {
		return err
	}
	if err := seal.WriteKey(k, *keyPath, *pubPath); err != nil {
		return err
	}
	fmt.Fprintf(out, "made the box's key: %s\n", seal.Fingerprint(k.PublicKey()))
	return nil
}

// boxKeyPath is where the harness's service finds the box's private key: systemd's credentials
// folder, which only this service can read.
func boxKeyPath() string {
	if d := os.Getenv("CREDENTIALS_DIRECTORY"); d != "" {
		return filepath.Join(d, "box.key")
	}
	return ""
}

// keyOpener is agent.Options.Key: the sealed copy in <state>/inbox/keys/<slug>.json, opened with
// the box's key for this listing and its server's address, and turned into what the gateway
// sends. No file is no key. Its errors are a few fixed sentences; they go in the journal.
func keyOpener(state string, box *ecdh.PrivateKey, ua string, lim gateway.Limits) func(context.Context, targets.Target) (*gateway.Credential, error) {
	return func(ctx context.Context, t targets.Target) (*gateway.Credential, error) {
		b, err := readSmall(journal.KeyFile(state, t.Slug), 32<<10)
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		if err != nil {
			return nil, errors.New("the sealed key couldn't be read")
		}
		if box == nil {
			return nil, errors.New("there's a sealed key, but the box's own key isn't loaded (LoadCredential in anchor-lab.service)")
		}
		var x seal.Sealed
		if json.Unmarshal(b, &x) != nil {
			return nil, errors.New("the sealed key isn't one")
		}
		s, err := seal.Open(box, x, t.Slug, t.URL)
		if err != nil {
			return nil, fmt.Errorf("the sealed key doesn't open for this server: %w", err)
		}
		switch s.Kind {
		case "header":
			return &gateway.Credential{Header: s.Name, Value: s.Prefix + s.Value}, nil
		case "query":
			return &gateway.Credential{Query: s.Name, Value: s.Value}, nil
		case "oauth-client":
			c, err := gateway.ClientToken(ctx, s.TokenURL, s.ClientID, s.ClientSecret, s.Scope, ua, lim)
			if err != nil {
				return nil, fmt.Errorf("no token for the OAuth client: %w", err)
			}
			return c, nil
		}
		return nil, errors.New("a kind of key the gateway doesn't send")
	}
}

func readSmall(path string, max int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, max+1))
	if err == nil && int64(len(b)) > max {
		err = errors.New("too big")
	}
	return b, err
}
