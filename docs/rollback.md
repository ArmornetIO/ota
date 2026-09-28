# Rolling machines back to an older release

A version that passed its health gate and later turned out bad is rolled
back with a **new offer, signed by the publisher, naming the older version**.
The server cannot do this by itself: it holds no key. Clients refuse an older
version unless the offer says `"downgrade": true`, and refuse any offer not
newer than the last one they acted on, so replaying the original offer for
the old release does nothing.

## Steps

1. Take the old release's offer and re-issue it as a downgrade:

   ```sh
   gh release download v1.7.0 -p ota-offer.json -O old.json
   ota offer -from old.json -downgrade -out ota-offer-rollback.json
   cosign sign-blob --yes --bundle ota-offer-rollback.json.sigstore.json ota-offer-rollback.json
   ```

   `-from` keeps the old artifacts (same bytes, same hashes) and channels,
   and gives the offer a fresh issue time, which is what lets clients accept
   it after the newer one.

2. Publish it where the server ingests from. Attaching it to the old release
   works, because a release may carry several `ota-offer*.json` files:

   ```sh
   gh release upload v1.7.0 ota-offer-rollback.json ota-offer-rollback.json.sigstore.json
   ```

   or post it straight to the server:

   ```sh
   jq -n --rawfile o ota-offer-rollback.json --rawfile s ota-offer-rollback.json.sigstore.json \
     '{offer: ($o|@base64), signature: ($s|@base64), signature_kind: "sigstore"}' |
   curl -X POST -H "Authorization: Bearer $ADMIN" --data @- https://updates.example.com/v1/admin/offers
   ```

3. Point the rollout at the old version:

   ```sh
   curl -X PUT -H "Authorization: Bearer $ADMIN" -d '{"version":"1.7.0","percent":100}' \
     https://updates.example.com/v1/admin/rollouts/myapp/stable
   ```

Machines then install 1.7.0 through the normal path, health gate included.
`examples/rollback.yml` is the same as a manually triggered workflow, so the
signature comes from CI rather than a laptop.
