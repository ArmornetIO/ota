// Command sampleapp is the program the fault-injection and CLI suites update.
// Its version and behaviour are set at build time, so v1 and v2 are two builds
// of the same source; everything else comes from the environment, which
// survives the exec into the new version.
//
//	sampleapp            check, update, exit (the one-shot in-process path)
//	sampleapp serve      stay running until SIGTERM (a daemon, for `run`)
//	sampleapp update     ota's one-shot CLI update
//	sampleapp run -- …   ota's resident launcher
package main

import (
	"context"
	stded "crypto/ed25519"
	"encoding/base64"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ArmornetIO/ota"
	"github.com/ArmornetIO/ota/cli"
	"github.com/ArmornetIO/ota/client"
	"github.com/ArmornetIO/ota/health"
	"github.com/ArmornetIO/ota/restart"
	"github.com/ArmornetIO/ota/verify/ed25519"
)

var (
	version   = "0.0.0"
	behaviour = "ok" // ok | unhealthy | crash
)

func note(format string, args ...any) {
	f, err := os.OpenFile(os.Getenv("OTA_MARKER"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600) // #nosec G703 G304 -- test fixture; the harness sets its own marker path
	if err != nil {
		panic(err)
	}
	fmt.Fprintf(f, format+"\n", args...)
	_ = f.Close()
}

func options() (client.Options, error) {
	key, err := base64.StdEncoding.DecodeString(os.Getenv("OTA_PUBKEY"))
	if err != nil {
		return client.Options{}, err
	}
	v, err := ed25519.New(stded.PublicKey(key))
	if err != nil {
		return client.Options{}, err
	}
	status := health.StatusOK
	if behaviour == "unhealthy" {
		status = health.StatusFatalError
	}
	var lastStage ota.Stage
	return client.Options{
		Program:        "sampleapp",
		ClientID:       "test-client",
		CurrentVersion: version,
		Transport:      &client.HTTPTransport{BaseURL: os.Getenv("OTA_SERVER")},
		Verifier:       v,
		Health:         health.StatusFunc(func() string { return status }),
		SteadyWindow:   200 * time.Millisecond,
		HealthPoll:     20 * time.Millisecond,
		CommitDeadline: 5 * time.Second,
		OnEvent: func(e ota.Event) {
			if e.Stage != lastStage {
				note("event %s %s", e.Stage, e.Version)
				lastStage = e.Stage
			}
		},
	}, nil
}

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "update", "run", "offer":
			cmds := cli.Commands(func() (cli.Config, error) {
				o, err := options()
				return cli.Config{
					Client: o,
					Launcher: restart.LauncherOptions{
						BackoffMin: 50 * time.Millisecond, BackoffMax: 200 * time.Millisecond,
						StopTimeout: 5 * time.Second, Poll: 50 * time.Millisecond,
					},
				}, err
			})
			os.Exit(cmds[os.Args[1]](os.Args[2:]))
		case "serve":
			serve()
			return
		}
	}
	oneShot()
}

func newClient() *client.Client {
	o, err := options()
	if err != nil {
		panic(err)
	}
	c, err := client.New(o)
	if err != nil {
		panic(err)
	}
	return c
}

func boot(c *client.Client) client.BootResult {
	br, err := c.Boot(context.Background())
	if err != nil {
		note("boot-error %v", err)
		os.Exit(1)
	}
	note("boot %s", version)
	if behaviour == "crash" {
		os.Exit(3)
	}
	return br
}

// oneShot is the in-process path: check, update (re-exec), exit.
func oneShot() {
	c := newClient()
	br := boot(c)
	if br.Gate != nil {
		r := <-br.Gate
		note("gate %s %s", r.Stage, r.Version)
		return
	}
	r, err := c.CheckAndUpdate(context.Background())
	switch {
	case err != nil:
		note("update %s %s %v", r.Stage, r.Version, err)
	case r.UpToDate:
		note("update uptodate %s", version)
	default:
		note("update %s %s", r.Stage, r.Version)
	}
}

// serve is a daemon: it resolves its own health gate, then runs until stopped.
// With -listen it also serves its version over HTTP on a shared port, which is
// what the side-by-side service test measures.
func serve() {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	listen := fs.String("listen", "", "serve the version over HTTP on this address (SO_REUSEPORT)")
	_ = fs.Parse(os.Args[2:])

	if *listen != "" {
		ln, err := listenShared(*listen)
		if err != nil {
			panic(err)
		}
		mux := http.NewServeMux()
		mux.HandleFunc("/", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, version) })
		mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
			fmt.Fprintf(w, `{"status":"OK","version":%q}`, version)
		})
		go func() { _ = (&http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}).Serve(ln) }()
	}

	// A service started without the fixture's environment has no updater to run.
	if os.Getenv("OTA_PUBKEY") != "" {
		c := newClient()
		br := boot(c)
		if br.Gate != nil {
			go func() {
				r := <-br.Gate
				note("gate %s %s", r.Stage, r.Version)
			}()
		}
	}
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, os.Interrupt, syscall.SIGTERM)
	<-sigs
	if os.Getenv("OTA_MARKER") != "" {
		note("stopped %s", version)
	}
}
