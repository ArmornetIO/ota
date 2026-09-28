package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ArmornetIO/ota"
)

// goreleaserArtifact is the subset of dist/artifacts.json that matters here.
type goreleaserArtifact struct {
	Name   string         `json:"name"`
	Path   string         `json:"path"`
	GOOS   string         `json:"goos"`
	GOARCH string         `json:"goarch"`
	Type   string         `json:"type"`
	Extra  map[string]any `json:"extra"`
}

type goreleaserMetadata struct {
	ProjectName string `json:"project_name"`
	Tag         string `json:"tag"`
	Version     string `json:"version"`
}

// runOffer writes an unsigned ota-offer.json. It never signs: the publisher
// signs the file with the tool they already use (cosign, goreleaser's signs).
func runOffer(args []string) int {
	fs := flag.NewFlagSet("offer", flag.ContinueOnError)
	dist := fs.String("dist", "dist", "goreleaser dist directory")
	from := fs.String("from", "", "build from an existing ota-offer.json instead of dist (a rollback to that release)")
	checksums := fs.String("checksums", "", "build from a goreleaser checksums.txt instead of artifacts.json; needs -program, -version and binary archives named <program>_<os>_<arch>")
	tag := fs.String("tag", "", "release tag, for -url-template with -checksums (default v<version>)")
	program := fs.String("program", "", "program name (default: goreleaser project name)")
	version := fs.String("version", "", "version (default: goreleaser metadata, or the -from offer's)")
	channels := fs.String("channels", "", "comma-separated channels the offer is for (default: all)")
	expiresIn := fs.Duration("expires-in", 0, "offer lifetime; 0 means it never expires")
	downgrade := fs.Bool("downgrade", false, "allow installing over a newer version (required for a rollback)")
	urlTemplate := fs.String("url-template", "", "mirror URL per artifact; {tag}, {version} and {name} are substituted")
	buildID := fs.String("id", "", "only use goreleaser artifacts from this build id")
	out := fs.String("out", "", "output file (default: <dist>/ota-offer.json)")
	issuedAt := fs.Int64("issued-at", 0, "issue time in Unix ms (default: now); for reproducible tests")
	if err := fs.Parse(args); err != nil {
		return ExitError
	}

	var o ota.Offer
	var err error
	switch {
	case *from != "":
		o, err = offerFromOffer(*from)
	case *checksums != "":
		t := *tag
		if t == "" {
			t = "v" + strings.TrimPrefix(*version, "v")
		}
		o, err = offerFromChecksums(*checksums, *program, *version, t, *urlTemplate)
	default:
		o, err = offerFromDist(*dist, *buildID, *urlTemplate)
	}
	if err != nil {
		return fail("offer: %v", err)
	}
	if *program != "" {
		o.Program = *program
	}
	if *version != "" {
		o.Version = strings.TrimPrefix(*version, "v")
	}
	o.Schema = ota.OfferSchema
	o.IssuedAt = *issuedAt
	if o.IssuedAt == 0 {
		o.IssuedAt = time.Now().UnixMilli()
	}
	o.ExpiresAt = 0
	if *expiresIn > 0 {
		o.ExpiresAt = time.UnixMilli(o.IssuedAt).Add(*expiresIn).UnixMilli()
	}
	o.Downgrade = *downgrade
	if *channels != "" {
		o.Channels = strings.Split(*channels, ",")
	} else if *from == "" {
		o.Channels = nil
	}

	b, err := json.MarshalIndent(o, "", "  ")
	if err != nil {
		return fail("offer: %v", err)
	}
	b = append(b, '\n')
	// Parse what will be signed, so a publisher never signs an offer every
	// client would refuse.
	if _, err := ota.ParseOffer(b); err != nil {
		return fail("offer: %v", err)
	}
	path := *out
	if path == "" {
		path = filepath.Join(*dist, "ota-offer.json")
	}
	// A release asset, published on purpose: world-readable is correct.
	if err := os.WriteFile(path, b, 0o644); err != nil { // #nosec G306
		return fail("offer: %v", err)
	}
	fmt.Fprintf(os.Stderr, "wrote %s: %s %s, %d artifact(s)\n", path, o.Program, o.Version, len(o.Artifacts))
	return ExitOK
}

func offerFromDist(dist, buildID, urlTemplate string) (ota.Offer, error) {
	var arts []goreleaserArtifact
	if err := readJSON(filepath.Join(dist, "artifacts.json"), &arts); err != nil {
		return ota.Offer{}, err
	}
	var meta goreleaserMetadata
	if err := readJSON(filepath.Join(dist, "metadata.json"), &meta); err != nil {
		return ota.Offer{}, err
	}

	// Binary-format archives are what goreleaser uploads as plain executables,
	// so their names match release assets; raw build outputs are the fallback.
	pick := func(match func(goreleaserArtifact) bool) []goreleaserArtifact {
		var out []goreleaserArtifact
		for _, a := range arts {
			if match(a) && (buildID == "" || extraString(a, "ID") == buildID) {
				out = append(out, a)
			}
		}
		return out
	}
	chosen := pick(func(a goreleaserArtifact) bool { return a.Type == "Archive" && extraString(a, "Format") == "binary" })
	if len(chosen) == 0 {
		chosen = pick(func(a goreleaserArtifact) bool { return a.Type == "Binary" })
	}
	if len(chosen) == 0 {
		return ota.Offer{}, errors.New("no binaries in artifacts.json")
	}

	o := ota.Offer{Program: meta.ProjectName, Version: strings.TrimPrefix(meta.Version, "v")}
	seen := map[string]string{}
	for _, a := range chosen {
		key := a.GOOS + "/" + a.GOARCH
		if prev, dup := seen[key]; dup {
			return ota.Offer{}, fmt.Errorf("two binaries for %s (%s, %s); pick one build with -id", key, prev, a.Name)
		}
		seen[key] = a.Name
		sum, size, err := hashFile(resolve(dist, a.Path))
		if err != nil {
			return ota.Offer{}, err
		}
		art := ota.Artifact{OS: a.GOOS, Arch: a.GOARCH, SHA256: sum, Size: size}
		if urlTemplate != "" {
			art.URLs = []string{strings.NewReplacer("{tag}", meta.Tag, "{version}", o.Version, "{name}", a.Name).Replace(urlTemplate)}
		}
		o.Artifacts = append(o.Artifacts, art)
	}
	return o, nil
}

// offerFromChecksums builds the offer from checksums.txt, which exists while
// goreleaser's signs step runs; artifacts.json is only written at the very end
// of a run, too late to sign and upload an offer from inside goreleaser. The
// binaries must be binary-format archives named <program>_<os>_<arch>[.exe].
func offerFromChecksums(path, program, version, tag, urlTemplate string) (ota.Offer, error) {
	if program == "" || version == "" {
		return ota.Offer{}, errors.New("-checksums needs -program and -version")
	}
	b, err := os.ReadFile(path) // #nosec G304 -- release CLI reading the publisher's own checksums file
	if err != nil {
		return ota.Offer{}, err
	}
	o := ota.Offer{Program: program, Version: strings.TrimPrefix(version, "v")}
	dir := filepath.Dir(path)
	for _, line := range strings.Split(string(b), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			continue
		}
		sum, name := fields[0], fields[1]
		goos, goarch, ok := platformOf(program, name)
		if !ok {
			continue
		}
		fi, err := os.Stat(filepath.Join(dir, name)) // #nosec G703 -- names from the publisher's own checksums.txt
		if err != nil {
			return ota.Offer{}, fmt.Errorf("%s is in checksums.txt but not beside it: %w", name, err)
		}
		art := ota.Artifact{OS: goos, Arch: goarch, SHA256: sum, Size: fi.Size()}
		if urlTemplate != "" {
			art.URLs = []string{strings.NewReplacer("{tag}", tag, "{version}", o.Version, "{name}", name).Replace(urlTemplate)}
		}
		o.Artifacts = append(o.Artifacts, art)
	}
	if len(o.Artifacts) == 0 {
		return ota.Offer{}, fmt.Errorf("no %s_<os>_<arch> binaries in %s", program, path)
	}
	return o, nil
}

// platformOf parses <program>_<os>_<arch>[.exe]. Only GOOS/GOARCH values Go
// ships are accepted, so archive names that merely look similar are skipped.
func platformOf(program, name string) (string, string, bool) {
	rest, ok := strings.CutPrefix(strings.TrimSuffix(name, ".exe"), program+"_")
	if !ok {
		return "", "", false
	}
	goos, goarch, ok := strings.Cut(rest, "_")
	if !ok || !knownOS[goos] || !knownArch[goarch] {
		return "", "", false
	}
	return goos, goarch, true
}

var (
	knownOS   = map[string]bool{"linux": true, "darwin": true, "windows": true, "freebsd": true, "netbsd": true, "openbsd": true}
	knownArch = map[string]bool{"amd64": true, "arm64": true, "386": true, "arm": true, "riscv64": true, "ppc64le": true, "s390x": true}
)

// offerFromOffer reuses an older release's artifacts, for a rollback offer.
func offerFromOffer(path string) (ota.Offer, error) {
	b, err := os.ReadFile(path) // #nosec G304 -- release CLI reading the publisher's prior offer
	if err != nil {
		return ota.Offer{}, err
	}
	o, err := ota.ParseOffer(b)
	if err != nil {
		return ota.Offer{}, err
	}
	o.TrustRootUpdate = nil
	return o, nil
}

// resolve finds a goreleaser path, which is relative to the project root
// (dist's parent), not to dist.
func resolve(dist, p string) string {
	if filepath.IsAbs(p) {
		return p
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dist), p)); err == nil {
		return filepath.Join(filepath.Dir(dist), p)
	}
	return filepath.Join(dist, p)
}

func hashFile(p string) (string, int64, error) {
	f, err := os.Open(p) // #nosec G304 -- release CLI hashing the publisher's own artifacts
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func extraString(a goreleaserArtifact, key string) string {
	s, _ := a.Extra[key].(string)
	return s
}

func readJSON(p string, v any) error {
	b, err := os.ReadFile(p) // #nosec G304 -- release CLI reading goreleaser output
	if err != nil {
		return err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("%s: %w", filepath.Base(p), err)
	}
	return nil
}
