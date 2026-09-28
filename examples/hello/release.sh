#!/bin/sh
# Builds hello for every platform, writes the offer and signs it with an
# ed25519 key (created on first run as hello.key, never commit it).
#
#   ./release.sh 1.0.0 https://updates.example.com [server-data-dir]
#
# Given the server's data directory it also drops the binaries straight into
# its blob store, so uploading the offer needs no download.
#
# Output in dist/: the binaries, ota-offer.json, ota-offer.json.sig and
# upload.json (the body for POST /v1/admin/offers).
set -eu
cd "$(dirname "$0")"
VERSION=${1:?usage: release.sh <version> [server-url]}
SERVER=${2:-http://localhost:8080}
DATA=${3:-}

[ -f hello.key ] || openssl genpkey -algorithm ed25519 -out hello.key
PUB=$(openssl pkey -in hello.key -pubout -outform DER | tail -c 32 | base64)

rm -rf dist && mkdir dist
for p in linux/amd64 linux/arm64 linux/arm darwin/amd64 darwin/arm64 windows/amd64 windows/arm64; do
	os=${p%/*} arch=${p#*/} ext=
	[ "$os" = windows ] && ext=.exe
	CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath \
		-ldflags "-s -w -X main.version=$VERSION -X main.publicKey=$PUB -X main.serverURL=$SERVER" \
		-o "dist/hello_${os}_${arch}${ext}" .
done

(cd dist && shasum -a 256 hello_* > checksums.txt)
# No -url-template: the offer lists no mirrors, so the server must already
# hold every artifact, keyed by hash. Real releases point these at the
# release page instead and let the server fetch them.
(cd ../../cmd/ota && go run . offer -checksums "$OLDPWD/dist/checksums.txt" \
	-program hello -version "$VERSION" -channels stable -out "$OLDPWD/dist/ota-offer.json")
if [ -n "$DATA" ]; then
	mkdir -p "$DATA/blobs"
	while read -r sum name; do cp "dist/$name" "$DATA/blobs/$sum"; done < dist/checksums.txt
fi
openssl pkeyutl -sign -rawin -inkey hello.key -in dist/ota-offer.json | base64 > dist/ota-offer.json.sig

# SignedOffer's byte fields are base64 in JSON; the ed25519 signature is
# itself base64 text, so it is encoded twice.
printf '{"offer":"%s","signature":"%s","signature_kind":"ed25519"}\n' \
	"$(base64 < dist/ota-offer.json | tr -d '\n')" \
	"$(base64 < dist/ota-offer.json.sig | tr -d '\n')" > dist/upload.json
echo "$PUB" > dist/hello.pub
echo "built hello $VERSION in dist/ (public key $PUB)"
