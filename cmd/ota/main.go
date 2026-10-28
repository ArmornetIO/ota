// Command ota is the standalone release server and publisher CLI.
//
//	ota serve -data-dir /var/lib/ota -source github -repo owner/app \
//	    -token-file clients.txt -admin-token-file admins.txt \
//	    -verifier sigstore -trusted-root trusted_root.json \
//	    -identity-issuer https://token.actions.githubusercontent.com \
//	    -identity-san-regex '^https://github.com/owner/app/\.github/workflows/release\.yml@refs/tags/v'
//	ota offer -dist dist -channels stable
package main

import (
	"bufio"
	"context"
	stded "crypto/ed25519"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/ArmornetIO/ota/cli"
	"github.com/ArmornetIO/ota/server"
	"github.com/ArmornetIO/ota/server/source/github"
	"github.com/ArmornetIO/ota/server/source/httpindex"
	"github.com/ArmornetIO/ota/verify"
	"github.com/ArmornetIO/ota/verify/ed25519"
	"github.com/ArmornetIO/ota/verify/sigstore"
)

const usage = `usage: ota <command> [flags]

commands:
  serve   run the release server
  offer   write an unsigned ota-offer.json from a goreleaser dist/ (see -h)
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	switch os.Args[1] {
	case "serve":
		if err := serve(os.Args[2:]); err != nil {
			fmt.Fprintf(os.Stderr, "ota serve: %v\n", err)
			os.Exit(1)
		}
	case "offer":
		os.Exit(cli.Commands(nil)["offer"](os.Args[2:]))
	default:
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
}

func serve(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	addr := fs.String("addr", ":8080", "listen address")
	dataDir := fs.String("data-dir", "", "state and artifact directory (required)")
	source := fs.String("source", "none", "release source: github, index or none")
	repo := fs.String("repo", "", "github owner/name")
	ghTokenFile := fs.String("github-token-file", "", "file holding a GitHub token (private repositories)")
	indexURL := fs.String("index-url", "", "static release index URL (source=index)")
	ingestEvery := fs.Duration("ingest-interval", 10*time.Minute, "how often to look for new releases; 0 disables polling")
	tokenFile := fs.String("token-file", "", "bearer tokens clients present, one per line")
	noAuth := fs.Bool("insecure-no-client-auth", false, "serve clients without authentication (only behind a proxy that authenticates)")
	adminTokenFile := fs.String("admin-token-file", "", "bearer tokens for the admin API, one per line; the admin API is off without it")
	verifier := fs.String("verifier", "sigstore", "offer signature format: sigstore or ed25519")
	trustedRoot := fs.String("trusted-root", "", "sigstore trusted_root.json")
	issuer := fs.String("identity-issuer", "", "sigstore: required OIDC issuer")
	san := fs.String("identity-san", "", "sigstore: required signer identity")
	sanRegex := fs.String("identity-san-regex", "", "sigstore: required signer identity pattern")
	edKeys := fs.String("ed25519-keys", "", "ed25519: file of trusted base64 public keys, one per line")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *dataDir == "" {
		return errors.New("-data-dir is required")
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	v, err := buildVerifier(*verifier, *trustedRoot, *issuer, *san, *sanRegex, *edKeys)
	if err != nil {
		return err
	}
	store, err := server.OpenFileStore(filepath.Join(*dataDir, "state"))
	if err != nil {
		return err
	}
	opts := server.Options{
		Store:    store,
		Blobs:    server.FSBlobStore{Dir: filepath.Join(*dataDir, "blobs")},
		Verifier: v,
		Logger:   log,
	}
	switch *source {
	case "github":
		tok := ""
		if *ghTokenFile != "" {
			if tok, err = readSecret(*ghTokenFile); err != nil {
				return err
			}
		}
		src, err := github.New(*repo, tok)
		if err != nil {
			return err
		}
		opts.Source = src
	case "index":
		if *indexURL == "" {
			return errors.New("-index-url is required for -source index")
		}
		opts.Source = &httpindex.Source{URL: *indexURL}
	case "none":
	default:
		return fmt.Errorf("unknown -source %q", *source)
	}

	switch {
	case *tokenFile != "":
		toks, err := readTokens(*tokenFile)
		if err != nil {
			return err
		}
		opts.Authorize = func(r *http.Request) (string, error) { return "", checkBearer(r, toks) }
	case !*noAuth:
		return errors.New("clients need authentication: pass -token-file, or -insecure-no-client-auth behind an authenticating proxy")
	}
	if *adminTokenFile != "" {
		toks, err := readTokens(*adminTokenFile)
		if err != nil {
			return err
		}
		opts.AuthorizeAdmin = func(r *http.Request) error { return checkBearer(r, toks) }
	}

	srv, err := server.New(opts)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if opts.Source != nil && *ingestEvery > 0 {
		go func() {
			for {
				// ponytail: one fixed bound so a stalled source cannot stop polling
				// for good; make it a flag if a release's artifacts take longer.
				ictx, cancel := context.WithTimeout(ctx, 30*time.Minute)
				res, err := srv.Ingest(ictx)
				cancel()
				if err != nil {
					log.Warn("ingest failed", "err", err)
				} else if res.Ingested+res.Rejected > 0 {
					log.Info("ingest", "ingested", res.Ingested, "rejected", res.Rejected, "errors", res.Errors)
				}
				select {
				case <-ctx.Done():
					return
				case <-time.After(*ingestEvery):
				}
			}
		}()
	}

	// ReadHeaderTimeout only: artifact downloads are long streams, and a body
	// or write deadline would cut them off mid-transfer.
	hs := &http.Server{Addr: *addr, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = hs.Shutdown(sctx)
	}()
	log.Info("ota server listening", "addr", *addr)
	if err := hs.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

func buildVerifier(kind, trustedRoot, issuer, san, sanRegex, edKeys string) (verify.Verifier, error) {
	switch kind {
	case "sigstore":
		if trustedRoot == "" {
			return nil, errors.New("-trusted-root is required for sigstore")
		}
		b, err := os.ReadFile(trustedRoot) // #nosec G304 -- operator's -trusted-root flag
		if err != nil {
			return nil, err
		}
		return sigstore.New(b, sigstore.Policy{Issuer: issuer, SAN: san, SANRegex: sanRegex})
	case "ed25519":
		lines, err := readTokens(edKeys)
		if err != nil {
			return nil, err
		}
		var keys []stded.PublicKey
		for _, l := range lines {
			k, err := base64.StdEncoding.DecodeString(string(l))
			if err != nil {
				return nil, fmt.Errorf("ed25519 key: %w", err)
			}
			keys = append(keys, stded.PublicKey(k))
		}
		return ed25519.New(keys...)
	}
	return nil, fmt.Errorf("unknown -verifier %q", kind)
}

// checkBearer compares in constant time against every configured token.
func checkBearer(r *http.Request, tokens [][]byte) error {
	got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok || got == "" {
		return errors.New("missing bearer token")
	}
	match := 0
	for _, t := range tokens {
		match |= subtle.ConstantTimeCompare([]byte(got), t)
	}
	if match != 1 {
		return errors.New("bad bearer token")
	}
	return nil
}

func readTokens(path string) ([][]byte, error) {
	if path == "" {
		return nil, errors.New("no file given")
	}
	f, err := os.Open(path) // #nosec G304 -- operator's token file
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out [][]byte
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if l := strings.TrimSpace(sc.Text()); l != "" && !strings.HasPrefix(l, "#") {
			out = append(out, []byte(l))
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s holds no entries", path)
	}
	return out, sc.Err()
}

func readSecret(path string) (string, error) {
	b, err := os.ReadFile(path) // #nosec G304 -- operator's secret file
	return strings.TrimSpace(string(b)), err
}
