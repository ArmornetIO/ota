# hello

A program that updates itself. Run 1.0.0 on a device, publish 1.1.0, and
watch it become 1.1.0 on its own.

```sh
# 1. Build every platform for 1.0.0; put the right binary on the device.
./release.sh 1.0.0 http://<server-host>:8089
scp dist/hello_linux_arm64 device:hello

# 2. Run the update server (go build -o ota ../../cmd/ota).
cp dist/hello.pub keys; echo devtoken > tok; echo admintoken > admin
ota serve -addr :8089 -data-dir data -verifier ed25519 -ed25519-keys keys \
  -token-file tok -admin-token-file admin

# 3. On the device.
OTA_TOKEN=devtoken ./hello -every 10s

# 4. Publish 1.1.0 and roll it out. Passing the server's data directory puts
#    the binaries in its blob store, so the upload is just the signed offer.
./release.sh 1.1.0 http://<server-host>:8089 data
curl -H 'Authorization: Bearer admintoken' -d @dist/upload.json localhost:8089/v1/admin/offers
curl -X PUT -H 'Authorization: Bearer admintoken' -d '{"version":"1.1.0","percent":100}' \
  localhost:8089/v1/admin/rollouts/hello/stable
```

The device logs `downloading`, `installed`, then `hello 1.1.0`, and after
30 seconds of running cleanly, `committed`. `release.sh` creates `hello.key`
on first run; keep it, since every build pins its public key.
