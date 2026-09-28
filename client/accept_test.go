package client

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ArmornetIO/ota"
)

func offer(mod func(*ota.Offer)) ota.Offer {
	o := ota.Offer{
		Schema:   ota.OfferSchema,
		Program:  "demo",
		Version:  "1.8.0",
		IssuedAt: time.Unix(2000, 0).UnixMilli(),
		Artifacts: []ota.Artifact{{
			OS: "linux", Arch: "amd64", SHA256: strings.Repeat("a", 64), Size: 10,
		}},
	}
	if mod != nil {
		mod(&o)
	}
	b, _ := json.Marshal(o)
	parsed, err := ota.ParseOffer(b)
	if err != nil {
		panic(err)
	}
	return parsed
}

func TestAccept(t *testing.T) {
	base := acceptance{
		program: "demo", channel: "stable", goos: "linux", goarch: "amd64",
		running:   ota.MustParseVersion("1.7.0"),
		lastIssue: time.Unix(1000, 0).UnixMilli(),
		now:       time.Unix(3000, 0),
		tolerance: 5 * time.Minute,
	}
	cases := []struct {
		name     string
		mod      func(*ota.Offer)
		acc      func(*acceptance)
		refused  bool
		upToDate bool
	}{
		{name: "newer version accepted"},
		{name: "wrong program", mod: func(o *ota.Offer) { o.Program = "other" }, refused: true},
		{name: "replay of an older offer", acc: func(a *acceptance) { a.lastIssue = time.Unix(2000, 0).UnixMilli() }, refused: true},
		{name: "issued far in the future", mod: func(o *ota.Offer) { o.IssuedAt = time.Unix(3000+3600, 0).UnixMilli() }, refused: true},
		{name: "issued slightly in the future within tolerance", mod: func(o *ota.Offer) { o.IssuedAt = time.Unix(3000+60, 0).UnixMilli() }},
		{name: "expired", mod: func(o *ota.Offer) { o.ExpiresAt = time.Unix(2500, 0).UnixMilli() }, refused: true},
		{name: "not expired", mod: func(o *ota.Offer) { o.ExpiresAt = time.Unix(9000, 0).UnixMilli() }},
		{name: "other channel", mod: func(o *ota.Offer) { o.Channels = []string{"beta"} }, refused: true},
		{name: "listed channel", mod: func(o *ota.Offer) { o.Channels = []string{"beta", "stable"} }},
		{name: "same version is up to date", mod: func(o *ota.Offer) { o.Version = "1.7.0" }, upToDate: true},
		{name: "unmarked downgrade", mod: func(o *ota.Offer) { o.Version = "1.6.0" }, refused: true},
		{name: "marked downgrade", mod: func(o *ota.Offer) { o.Version = "1.6.0"; o.Downgrade = true }},
		{name: "no artifact for platform", acc: func(a *acceptance) { a.goarch = "arm64" }, refused: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a := base
			if c.acc != nil {
				c.acc(&a)
			}
			v, err := a.accept(offer(c.mod))
			if c.refused {
				if !errors.Is(err, ota.ErrRefused) {
					t.Fatalf("err = %v, want ErrRefused", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected refusal: %v", err)
			}
			if v.upToDate != c.upToDate {
				t.Fatalf("upToDate = %v, want %v", v.upToDate, c.upToDate)
			}
		})
	}
}
