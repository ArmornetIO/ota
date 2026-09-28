// Command hello is a program that keeps itself up to date: it prints its
// version, then checks the update server every -every and becomes the new
// version when a signed one is offered. Build it with release.sh.
package main

import (
	"context"
	stded "crypto/ed25519"
	"encoding/base64"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/ArmornetIO/ota"
	"github.com/ArmornetIO/ota/client"
	"github.com/ArmornetIO/ota/health"
	"github.com/ArmornetIO/ota/verify/ed25519"
)

// Set with -ldflags -X at build time. The key is pinned in the binary so a
// compromised server cannot hand this device an update the publisher never signed.
var (
	version   = "0.0.0"
	publicKey = ""
	serverURL = "http://localhost:8080"
)

func main() {
	every := flag.Duration("every", time.Minute, "how often to check for an update")
	token := flag.String("token", os.Getenv("OTA_TOKEN"), "bearer token for the update server")
	flag.Parse()

	key, err := base64.StdEncoding.DecodeString(publicKey)
	if err != nil {
		log.Fatalf("publicKey: %v", err)
	}
	v, err := ed25519.New(stded.PublicKey(key))
	if err != nil {
		log.Fatal(err)
	}
	id, _ := os.Hostname()
	var last ota.Stage
	c, err := client.New(client.Options{
		Program:        "hello",
		ClientID:       id,
		CurrentVersion: version,
		Transport: &client.HTTPTransport{BaseURL: serverURL, Authorize: func(r *http.Request) error {
			r.Header.Set("Authorization", "Bearer "+*token)
			return nil
		}},
		Verifier: v,
		// Reaching the loop is this program's whole job, so reaching it is healthy.
		Health: health.StatusFunc(func() string { return health.StatusOK }),
		// Downloads emit an event per chunk; one line per stage is enough.
		OnEvent: func(e ota.Event) {
			if e.Stage != last {
				last = e.Stage
				log.Printf("ota: %s %s", e.Stage, e.Version)
			}
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	ctx := context.Background()
	if _, err := c.Boot(ctx); err != nil {
		log.Fatal(err)
	}

	log.Printf("hello %s", version)
	for {
		// On an update this does not return: the process becomes the new version.
		// ErrLocked while a new version waits out its health gate is expected.
		if _, err := c.CheckAndUpdate(ctx); err != nil && !errors.Is(err, ota.ErrLocked) {
			log.Printf("update: %v", err)
		}
		time.Sleep(*every)
	}
}
