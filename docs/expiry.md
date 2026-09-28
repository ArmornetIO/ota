# Offer expiry (optional)

By default an offer never expires. That keeps publishing to one CI step, and
it leaves one risk open: a compromised release server can withhold newer
offers and keep machines on an older, still validly signed version. It
cannot install anything unsigned or replay an old offer; it can only stop
telling machines about new ones.

If that risk matters to you, give offers a lifetime and re-sign the current
one on a schedule shorter than it:

```sh
ota offer ... -expires-in 720h     # 30 days
```

Clients refuse an expired offer (with a 5-minute clock tolerance), so a
server that stops serving fresh offers is noticed within the lifetime rather
than never.

`examples/resign-offer.yml` is a scheduled workflow that re-issues the
current release's offer every week with a new issue time and expiry, signs
it, and attaches it to the same release. The server ingests it like any
other offer, and clients take it because it is newer. If the job stops
running, machines stop updating once the last offer expires. That is the
point: silence becomes visible.
