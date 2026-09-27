// OpenFlux script-transport template.
//
// A script transport is one .js file that implements everything a native
// Go transport would: the auth/handshake flow, wire framing, reconnect and
// keepalive policy. It is loaded into its own goja runtime (one per
// transport instance) and gets an unrestricted host API - there is no
// capability sandboxing. The only gate is the signature check the Go side
// runs before this file is ever evaluated: it must ship as
// `<name>.js` + `<name>.js.sig`, signed with a key the exit/node trusts
// (see transport/script/sign.go, cmd/scriptsign).
//
// Everything below is the full contract. Delete what you don't need,
// but keep the shape: a global `Transport` object with these members.

var Transport = {

  // info() -> manifest object. Called once, synchronously, right after
  // this file is evaluated - before open(). All fields optional except
  // `name`.
  info: function () {
    return {
      name: "template",
      version: "1.0.0",

      // Base URL cookieJar.get()/set() read and write cookies against.
      // Leave empty if this transport doesn't need cookies.
      cookieDomain: "https://example.com/",

      // Advisory only. The core never fragments, reorders or rate-limits
      // on your behalf unless you ask - omit these (or leave at defaults)
      // to get plain passthrough: whatever byte string write() is called
      // with is exactly what core handed to Send(), uncapped.
      mtu: 0,             // 0 = unbounded
      reliable: false,    // does the underlying channel guarantee delivery?
      ordered: false,      // does it guarantee order?
      halfDuplex: false,
      minIntervalMs: 0,   // floor between sends, if the channel needs one

      // What this transport needs from the operator before open() can
      // work. Purely declarative - the manager/CLI/UI use this to build a
      // form; open()'s cfg.params is whatever they collected, keyed by
      // `key` below.
      params: [
        { key: "url", label: "Board URL", type: "url", required: true },
      ],
    };
  },

  // open(cfg) -> Promise|undefined. cfg = { url, params: {...} } - same
  // shape as a native transport's control.TransportConfig. Do all your
  // dialing/auth here. This is NOT awaited by the Go side: return (or let
  // an async function return) as soon as you've kicked off the connection,
  // the same way a native transport's Start() returns immediately and
  // finishes connecting on a background goroutine.
  //
  // Call setState("connected") the moment the link is actually usable -
  // IsConnected() on the Go side is driven ONLY by your setState() calls.
  // Call setState("connecting" | "reconnecting" | "degraded" | "dead", err)
  // at every other transition; the core has no other way to know your
  // link's health.
  open: async function (cfg) {
    setState("connecting");
    try {
      // Example: fetch a token, then open a WebSocket.
      // var res = await http.fetch({ url: cfg.url, method: "GET" });
      // if (res.status !== 200) throw new Error("http " + res.status);
      //
      // this._sock = await ws.open("wss://example.com/socket", {
      //   "User-Agent": "Mozilla/5.0",
      // });
      // this._sock.onmessage = onMessage;
      // this._sock.onclose = onClose;

      setState("connected");
    } catch (e) {
      setState("dead", String(e));
      // Your own reconnect policy goes here - e.g. setTimeout(() =>
      // Transport.open(cfg), backoffMs()). The core will not retry for you.
    }
  },

  // write(bytes) -> throws on failure, returns normally on success. Called
  // by Go once per queued packet, off Send()'s hot path (Go already
  // queues; this only runs when a packet is actually being sent). `bytes`
  // is an ArrayBuffer - the packet's raw bytes, no copy, no codec. Pass it
  // straight to a binary WS frame (sock.send(bytes)), or - if your wire
  // format needs text - base64.encode(bytes) first and embed the string.
  // Throwing causes Go to retry this same packet after a short backoff, so
  // it's safe (and expected) to throw when the socket momentarily isn't
  // open.
  write: function (bytes) {
    // if (!this._sock) throw new Error("not connected");
    // this._sock.send(bytes); // binary frame, zero-copy
    // this._sock.send(JSON.stringify({ type: "data", p: base64.encode(bytes) })); // text protocol
  },

  // close() -> called on Stop(). Clean up your own state; the core also
  // clears every setTimeout/setInterval you registered via Terminate(), so
  // you don't strictly need to clearTimeout yourself, but closing sockets
  // here avoids a lingering half-open connection until GC catches it.
  close: function () {
    // if (this._sock) this._sock.close();
  },

  // onEvent(kind, payload) -> optional. Downward OOB delivery: the host
  // application calls this (via ScriptTransport.Deliver on the Go side)
  // to hand you a captcha answer, fresh cookies, or an arbitrary inject.
  // Omit this entirely if your transport never needs it.
  onEvent: function (kind, payload) {
    // if (kind === "cookies") cookieJar.set(payload);
    // if (kind === "captchaAnswer") { ... }
  },
};

// ---- Host API available to every script (all globals, no import) ----
//
// http.fetch(opts) -> Promise<{status, url, body, headers}>
//   opts: { url, method, headers, body }. `url` in the result is the
//   FINAL url after redirects - check it against what you asked for to
//   detect a bounce to a login/captcha page.
//
// ws.open(url, headers) -> Promise<Socket>
//   Socket.send(text)             - text frame (JSON/Socket.IO protocols)
//   Socket.send(bytes)            - binary frame, bytes = ArrayBuffer/TypedArray
//   Socket.close()
//   Socket.onmessage = function(textOrBytes) {} - string for a text frame,
//                                                  ArrayBuffer for a binary one
//   Socket.onclose   = function(reason) {}
//   (assign these any time after open() resolves; they're read lazily
//   on every dispatch, so re-assigning mid-connection is fine)
//
// cookieJar.get() -> {name: value, ...}      (against info().cookieDomain)
// cookieJar.set({name: value, ...})
//
// base64.encode(bytes) -> string     base64.decode(string) -> ArrayBuffer
//   Only needed if YOUR wire format is textual (JSON, a cursor field, ...).
//   The write()/emit() packet boundary itself is always raw bytes.
//
// setTimeout/setInterval/clearTimeout/clearInterval, console.log/warn/error
//   - standard, run on this transport's own loop.
//
// emit(bytes)                 - deliver one received application packet up,
//                                bytes = ArrayBuffer/TypedArray, zero-copy
// setState(state, errMsg?)    - "connecting"|"connected"|"reconnecting"|
//                                "degraded"|"dead"
// raise(kind, payload)        - upward OOB event, e.g.
//                                raise("captchaRequired", {url: "..."})
//
// Nothing here is capability-scoped: dial anywhere, fetch anything. The
// only trust boundary is the signature on this file.
