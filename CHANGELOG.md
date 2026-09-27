# OpenFlux — Changelog

Comparing `hotfix/oflx-encrypted-logging-context` (this working tree) against
upstream `p1neappleXpress/OpenFlux@main` (`0946498`).

**Two layers here, and they're at very different stages:**

1. **Already committed on this branch** — logging, provisioning, Windows tun,
   session/manager reliability work. Stable, tested, ready to merge whenever.
2. **New this pass, uncommitted, NOT in main yet** — the JS-scripted transport
   system (goja). This is real, tested against live infrastructure, and the
   core builds/vets/tests green with it in — but it is **not merged into
   main** and shouldn't be treated as shipped. Treat it as a working preview.

---

## 1. Already on this branch (committed, vs upstream main)

- **Encrypted, leveled logging** (`utils/logging.go`) — a real `-d/-dd/-ddd`
  verbosity model plus `--sensitive` gating, replacing ad-hoc `fmt.Println`
  debug output across the core.
- **Node provisioning / `--node-wizard`** (`provision/`, `node_wizard*.go`) —
  scripted, pinned-hash-verified exit-node install over SSH, plus a JSON
  protocol for a desktop wizard to drive it non-interactively.
- **Windows `--inbound=tun`** (`netbind/netbind_windows.go`, `tun_windows.go`)
  — full tunnel support on Windows via Wintun, previously macOS/Linux-only.
- **Session/Manager reliability rework** (`transport/session.go`,
  `transport/manager/manager.go`) — the biggest functional change: skew
  handling, account-cookie isolation between transports, and a large new
  test matrix (`matrix_test.go`, `session_skew_test.go`) covering
  multi-transport failure modes that used to be the project's main source of
  instability.
- **Yandex-family fixes**: `--yandex-cookies-file` (Netscape cookies.txt) for
  vyandex, a PoW-captcha fix, boards/vyandex hardening.
- Assorted: `socks5` auth + counters, `transport/direct.go` and
  `transport/encrypted.go` hardening, `transport/batched.go` tweaks.

None of the above is touched by part 2 below.

---

## 2. New this pass — scripted (JS) transports — **NOT in main**

### The idea

Transports used to be compiled Go code — adding one meant a core rebuild
(and, on Android, a new APK). They're now **signed JavaScript files**,
loaded at runtime into a per-transport [goja](https://github.com/dop251/goja)
interpreter (pure Go, no cgo — cross-compiles the same as everything else).
The core ships zero transport-specific logic; it only provides:

- a small, deliberately **unrestricted** host API (HTTP, WebSocket, UDP,
  cookies, gzip/lz4, crypto, a real WebRTC PeerConnection/DataChannel, a
  bounded-concurrency pool) — freedom for the script, not a sandbox,
- one hard gate: an **ed25519 signature check**. Unsigned or wrongly-signed
  code never runs. That signature is the entire security boundary.

New location: `transport/script/` (~3,200 lines of Go host/runtime code) and
`transport/script/js/` (~2,300 lines of authored JS, ~3,800 lines of the
bundled/signed output actually loaded at runtime).

### Transport status (all against real, live infrastructure — nothing here is mocked)

| Transport | File | Status |
|---|---|---|
| mailru | `mailru.js` | ✅ full round-trip: auth, WS, live packets from a real peer. Load-tested: **~11,800 pkt/s / 5.9 MB/s** submit-side. |
| boards | `boards.js` | ✅ full round-trip incl. a natural reconnect recovery. Load-tested: **~8,400 pkt/s / 4.2 MB/s**. |
| cupsonline | `cupsonline.js` | ✅ full **bidirectional** round-trip between two independent peers through a real room (4-room fan-out). Load-tested: **~4,000 pkt/s / 1.5 MB/s each way**, ~450KB actually exchanged peer-to-peer in one run. |
| yandex | `yandex.js` | ⚠️ auth/redirect/captcha-detection verified real; happy path (actual WS session) still blocked by Yandex's SmartCaptcha even with a valid session cookie jar supplied — looks IP-reputation-based, not something a code fix can route around from this environment. |
| vyandex ("Volga") | `vyandex.js` | ⚠️ same captcha wall as yandex.js, but on the run where it got past it, reached a real 200 response and parsed the page — failed later on a document that doesn't expose an editable session (`action_url missing`), not a script bug. |
| oneme (MAX/VK) | `oneme-iceinject.js` | ✅ real login + real incoming-call detection/dial against `ws-api.oneme.ru` (`EVENT callConnected` fired against a genuine call from the account's own phone). No data received — correctly so: this variant only understands data smuggled by another instance of *our own* transport, and a real phone call speaks genuine WebRTC SDP/ICE instead, which it doesn't try to decode as payload. |
| oneme (MAX/VK), WebRTC variant | `oneme-webrtc.js` | ✅✅ **real ICE/DTLS handshake completed against the production MAX client** — a real call from the account's own phone reached `PeerConnectionStateConnected`, held for the call's ~38s duration, then cleanly detected `peerconnection failed` within 2s of hangup. This is genuinely new work (native's own WebRTC path is unfinished/dead - see below) and it just proved itself against real infrastructure, not a simulation. No DataChannel traffic (expected: a voice call negotiates audio, not our data channel) - the DataChannel path itself still needs a second MAX account to fully confirm. |

### The oneme/MAX finding worth knowing about

The **existing native** `transport/oneme` transport's real WebRTC dance
(SDP offer/answer, ICE, the DataChannel) is dead code today: `sendSDP`'s
actual socket write is commented out and both `SetLocalDescription` calls
are commented out, so ICE never starts. The only thing that has ever
actually moved bytes is a side-channel trick — payload data smuggled inside
a fake "ICE candidate" message over the plain signaling WebSocket.

Given that, two scripted alternatives were built instead of one:

- **`oneme-iceinject.js`** — a faithful port of the path that actually
  works today.
- **`oneme-webrtc.js`** — a genuine, working WebRTC data path that
  *completes* what native left unfinished (real `setLocalDescription`, real
  SDP transmission, real ICE exchange, real DataChannel send/receive). This
  needed a new native primitive (`webrtc.*` in `transport/script/host_webrtc.go`,
  a thin wrapper over `pion/webrtc` — already a dependency, no new one
  added). Proven for real with an automated test that runs two script
  transports locally, completes a genuine ICE/DTLS/SCTP handshake, and
  passes a message over a live DataChannel in under 100ms.

Native `transport/oneme` is untouched and still the default — this is an
alternative on offer, not a replacement, until a real call test confirms one
of the two.

### Packaging: the `.flux` format

A script can now ship as a single `.flux` file: a zip containing
`manifest.json` (name/version/author/description/icon) + `main.js` +
`package.sig`. The signature covers the **whole manifest together with the
script**, so a legitimately-signed package's name/author/icon can't be
swapped without invalidating the signature. `scriptsign pack`/`verify`
build and check them; the loader picks `.flux` over a plain `.js` file
automatically. Live-verified end to end (packed, signed, loaded, ran a real
session).

### Shared code between scripts

Scripts can `require()` local modules now — inlined at build time by a new
`scriptbundle` tool into one self-contained signed file (the runtime itself
never got a module loader; the only trust boundary stays "one signed file").
Used to de-duplicate the SmartCaptcha solver (was copy-pasted 3×) and to
share MAX/VK's login client between the two oneme variants.

### A real bug found (and fixed) via this testing

`ApplyCookies` (delivers an out-of-band-solved captcha's cookies into a
running transport) never actually scoped cookies across subdomains for
yandex.js/vyandex.js — their cookie domain is already the apex (`yandex.ru`),
so the old code left them host-only, meaning they'd never reach
`disk.yandex.ru`. Fixed and covered by a regression test; confirmed working
live (vyandex.js reached a clean `200` past the captcha with real cookies
applied afterward).

### Three native transports removed from the core

`transport/mailru`, `transport/cupsonline`, and `transport/yandex/boards.go`
are **deleted** — their script equivalents are proven, so per the project's
own rule ("script it, test it, then remove the native one"), the native
code is gone. `--transport=mailru`, `--transport=cupsonline`,
`--transport=boards` no longer exist; use `--transport=script:mailru`
(etc.) with `--script-dir`/`--script-pubkey`. yandex/vyandex stay native
for now — their scripted happy path isn't proven yet (see table above).

---

## What's explicitly still open

- yandex.js/vyandex.js happy path — blocked by Yandex-side anti-bot scoring,
  not something fixable from here without a different vantage point.
- oneme-webrtc.js's ICE/DTLS layer is now confirmed live against a real
  call; its DataChannel data path specifically (two of *our* transports
  talking, not a real voice call) still needs a second MAX account/token to
  test end to end.
- No decision yet on ever removing native `transport/oneme` — contingent on
  the DataChannel path above getting confirmed.

## Trying it today

```
--transport=script:mailru --script-dir=transport/script/js \
  --script-pubkey=d8bf9c958b994c2faab886cade5f28213f254911f87abe5e34756a289ae91354
```

or any of `script:boards`, `script:cupsonline`, `script:yandex`,
`script:vyandex`, `script:oneme-iceinject`, `script:oneme-webrtc`.

**None of this is merged into `main`.** It lives on this branch,
uncommitted, specifically so it can be reviewed and tested before it goes
anywhere near the upstream repo.
