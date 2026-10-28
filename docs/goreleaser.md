# Publishing updates with goreleaser

One block in `.goreleaser.yaml` and one permission in CI. Every tagged
release then carries a signed offer, and an ota server pointed at your
releases picks it up.

## 1. Ship binaries, not only archives

Clients download a raw executable, so publish one per platform as a
binary-format archive named `<project>_<os>_<arch>`:

```yaml
archives:
  - id: ota
    formats: [binary]
    name_template: "{{ .ProjectName }}_{{ .Os }}_{{ .Arch }}"
```

Keep your existing tar.gz/zip archives as another entry if you publish them.

## 2. Generate and sign the offer

```yaml
signs:
  - id: ota-offer
    artifacts: checksum        # runs once, after every binary is checksummed
    cmd: sh
    args:
      - -c
      - >-
        ota offer -checksums dist/checksums.txt
        -program {{ .ProjectName }} -version {{ .Version }} -tag {{ .Tag }}
        -channels stable -out dist/ota-offer.json
        -url-template "https://github.com/OWNER/REPO/releases/download/{tag}/{name}"
        && cosign sign-blob --yes --bundle dist/ota-offer.json.sigstore.json dist/ota-offer.json
    signature: ota-offer.json.sigstore.json
    output: true

release:
  extra_files:
    - glob: dist/ota-offer.json
```

- `ota offer` writes the offer (it never signs); `cosign sign-blob --bundle`
  signs it keyless with the workflow's identity and writes a bundle that
  carries the transparency-log proof clients verify offline.
- The offer is built from `checksums.txt` because goreleaser writes
  `artifacts.json` only after signing; do not use `-dist` here.
- `-url-template` makes every binary downloadable from the release as a
  mirror. The ota server copies artifacts from these URLs when it ingests.
- For a local key instead of Sigstore, sign with `ota`'s ed25519 format and
  publish `dist/ota-offer.json.sig` (see `verify/ed25519`).

## 3. Let CI sign

GitHub Actions needs an OIDC token for keyless signing:

```yaml
permissions:
  contents: write   # upload the release
  id-token: write   # cosign keyless signing
steps:
  - uses: sigstore/cosign-installer@v3
  # Built from a clone: cmd/ota resolves its sibling modules from the tree, so
  # `go install …@version` refuses it.
  - run: |
      git clone --depth 1 https://github.com/ArmornetIO/ota "$RUNNER_TEMP/ota"
      cd "$RUNNER_TEMP/ota/cmd/ota" && go build -o "$(go env GOPATH)/bin/ota" .
  - uses: goreleaser/goreleaser-action@v6
    with: { args: release --clean }
```

## 4. Point a server at it

```sh
ota serve -data-dir /var/lib/ota -source github -repo OWNER/REPO \
  -token-file clients.txt -admin-token-file admins.txt \
  -verifier sigstore -trusted-root trusted_root.json \
  -identity-issuer https://token.actions.githubusercontent.com \
  -identity-san-regex '^https://github\.com/OWNER/REPO/\.github/workflows/release\.yml@refs/tags/v'
```

The SAN pattern is what makes this secure: only your release workflow, run
for a tag, can produce an offer the server will store and clients will
accept. Fetch `trusted_root.json` once from the Sigstore TUF repository
(`cosign trusted-root create` or the sigstore-go `tuf` client) and pin it in
the server and, compiled in, in your clients.

Then start a rollout:

```sh
curl -X PUT -H "Authorization: Bearer $ADMIN" \
  -d '{"version":"1.8.0","percent":10}' \
  https://updates.example.com/v1/admin/rollouts/myapp/stable
```

## Checking it end to end

After a tag, `GET /v1/admin/releases` lists the version as `ingested`. If it
says `rejected`, the reason names what failed: a missing signature file,
the wrong signer identity, or an artifact whose bytes did not match.
