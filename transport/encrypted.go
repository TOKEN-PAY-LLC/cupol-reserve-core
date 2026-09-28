package transport

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/scrypt"

	"openflux/utils"
)

const (
	encryptedVersion = byte(1)
	encryptedHeader  = 5
	maxSeenNonces    = 4096
)

var encryptedMagic = [3]byte{'O', 'F', 'X'}

// Context fallback timing. A client that has not heard a single valid
// packet within contextFirstRotate of its first send moves on to the next
// candidate context every contextRotateEvery while it keeps sending; one
// that has, but then hears nothing for
// contextUnlockAfter while sending, starts trying again (the exit may have
// been reconfigured).
const (
	contextFirstRotate = 4 * time.Second
	contextRotateEvery = 2 * time.Second
	contextUnlockAfter = 2 * time.Minute
)

// EncryptedTransport wraps another Transport with end-to-end AES-256-GCM
// authenticated encryption, so the transport's own provider only ever
// observes ciphertext. Keys are derived from a shared secret via scrypt; the
// context string is just a public, per-session KDF salt - secrecy comes
// exclusively from the secret, which both peers must share out of band.
//
// Each direction (client->exit, exit->client) uses its own derived key, so a
// compromise of one direction's traffic does not help decrypt the other.
// Every packet also carries a random nonce and is checked against a bounded
// replay window, so a captured packet cannot be replayed back at either
// peer.
//
// Context fallback: builds have derived the context differently (see
// KDFContexts), and a mismatch used to drop every packet without a word.
// SetAlternateContexts gives the transport the contexts a peer may have
// used instead. A packet that fails under the current keys is tried under
// the others; the exit then answers under whichever the client used, and a
// client that hears nothing cycles through them until the exit answers.
// This weakens nothing: the context is a public salt and every candidate
// still requires the secret.
type EncryptedTransport struct {
	Transport
	ring *keyRing

	seenMu    sync.Mutex
	seen      map[string]struct{}
	seenOrder []string

	// Diagnostic counters.
	sendOK     atomic.Uint64
	sendErr    atomic.Uint64
	recvOK     atomic.Uint64
	recvBadLen atomic.Uint64
	recvBadHdr atomic.Uint64
	recvFail   atomic.Uint64
	recvReplay atomic.Uint64
}

// contextKeys are the AEADs derived for one candidate context.
type contextKeys struct {
	context string
	send    cipher.AEAD
	recv    cipher.AEAD
}

// keyRing holds the keys of every candidate context of one carrier and
// which of them this side currently sends under. Two EncryptedTransports on
// the same carrier (a Session's and its classic fallback's) share one ring,
// so what one learns about the peer's context the other uses too.
type keyRing struct {
	secret  string
	exit    bool
	side    string
	sendDir byte
	recvDir byte

	mu       sync.Mutex
	keys     []*contextKeys // [0] is the primary context
	pending  []string       // candidates not derived yet
	deriving bool
	send     int  // index into keys used for sending
	locked   bool // the peer answered under keys[send]
	heard    time.Time
	started  time.Time // first send while nothing was heard
	rotated  time.Time
	failures uint64 // packets no candidate could open since the last success
}

// NewEncryptedTransport wraps inner with a directional AES-256-GCM stream.
// Both peers must be configured with the same secret and context, and
// exactly one of them must set exitNode=true so the two sides pick opposite
// send/receive key pairs.
func NewEncryptedTransport(inner Transport, secret, context string, exitNode bool) (*EncryptedTransport, error) {
	if inner == nil {
		return nil, errors.New("inner transport is nil")
	}
	if utils.SecretChars(secret) < utils.MinSecretChars {
		return nil, fmt.Errorf("encryption secret must contain at least %d characters", utils.MinSecretChars)
	}
	r := &keyRing{secret: secret, exit: exitNode, side: "CLIENT", sendDir: 0, recvDir: 1}
	if exitNode {
		r.side, r.sendDir, r.recvDir = "EXIT", 1, 0
	}
	k, err := r.derive(context)
	if err != nil {
		return nil, err
	}
	r.keys = []*contextKeys{k}
	return &EncryptedTransport{Transport: inner, ring: r, seen: make(map[string]struct{})}, nil
}

// SharingKeys returns an EncryptedTransport over inner that shares e's keys
// and context state (not its replay window). Used for the classic fallback
// path that runs next to a Session on the same carrier.
func (e *EncryptedTransport) SharingKeys(inner Transport) *EncryptedTransport {
	return &EncryptedTransport{Transport: inner, ring: e.ring, seen: make(map[string]struct{})}
}

// SetAlternateContexts sets the contexts the peer may derive its keys from
// instead of the primary one (see KDFContexts). They are derived only when
// needed.
func (e *EncryptedTransport) SetAlternateContexts(contexts []string) {
	r := e.ring
	r.mu.Lock()
	defer r.mu.Unlock()
	have := make(map[string]bool)
	for _, k := range r.keys {
		have[k.context] = true
	}
	for _, c := range r.pending {
		have[c] = true
	}
	for _, c := range contexts {
		if c != "" && !have[c] {
			have[c] = true
			r.pending = append(r.pending, c)
		}
	}
	if len(r.pending) > 0 {
		utils.Debugf("[CRYPTO] side=%s %d alternate KDF context(s) on standby (derived on demand)", r.side, len(r.pending))
	}
}

// Context returns the context this side currently sends under.
func (e *EncryptedTransport) Context() string {
	r := e.ring
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.keys[r.send].context
}

func (r *keyRing) derive(context string) (*contextKeys, error) {
	salt := sha256.Sum256([]byte("OpenFlux encrypted transport v1\x00" + context))
	master, err := scrypt.Key([]byte(r.secret), salt[:], 32768, 8, 1, 32)
	if err != nil {
		return nil, fmt.Errorf("derive encryption key: %w", err)
	}
	clientToExit := deriveDirectionalKey(master, "client-to-exit")
	exitToClient := deriveDirectionalKey(master, "exit-to-client")
	sendKey, receiveKey := clientToExit, exitToClient
	if r.exit {
		sendKey, receiveKey = exitToClient, clientToExit
	}
	send, err := newGCM(sendKey)
	if err != nil {
		return nil, err
	}
	recv, err := newGCM(receiveKey)
	if err != nil {
		return nil, err
	}

	// [KEYDUMP] digest is emitted from debug level 2 (-dd): SHA-256
	// prefixes of the context and of the derived keys, so two peers can
	// compare derivations without revealing keys. It carries no hash of
	// the secret itself: that would let anyone with the log test guesses
	// without paying for scrypt. The detailed dump is --sensitive only.
	utils.Debugf("[KEYDUMP] digest side=%s context=%q contextSHA256=%s masterSHA256=%s c2eSHA256=%s e2cSHA256=%s",
		r.side, context,
		utils.Sha256Short([]byte(context)),
		utils.Sha256Short(master),
		utils.Sha256Short(clientToExit),
		utils.Sha256Short(exitToClient),
	)
	if utils.Sensitive() {
		utils.Debugf("[KEYDUMP] ============================================================")
		utils.Debugf("[KEYDUMP] side=%s", r.side)
		utils.Debugf("[KEYDUMP] secretLen=%d", len(r.secret))
		utils.Debugf("[KEYDUMP] secretSHA256=%s", utils.Sha256Hex([]byte(r.secret)))
		utils.Debugf("[KEYDUMP] secretHex(first 32)=%s", hex.EncodeToString([]byte(r.secret)[:minInt(len(r.secret), 32)]))
		utils.Debugf("[KEYDUMP] context=%q", context)
		utils.Debugf("[KEYDUMP] contextSHA256=%s", utils.Sha256Hex([]byte(context)))
		utils.Debugf("[KEYDUMP] salt=%s", hex.EncodeToString(salt[:]))
		utils.Debugf("[KEYDUMP] master=%s", hex.EncodeToString(master))
		utils.Debugf("[KEYDUMP] client->exit=%s", hex.EncodeToString(clientToExit))
		utils.Debugf("[KEYDUMP] exit->client=%s", hex.EncodeToString(exitToClient))
		utils.Debugf("[KEYDUMP] sendDir=%d recvDir=%d", r.sendDir, r.recvDir)
		utils.Debugf("[KEYDUMP] sendKey=%s", hex.EncodeToString(sendKey))
		utils.Debugf("[KEYDUMP] recvKey=%s", hex.EncodeToString(receiveKey))
		utils.Debugf("[KEYDUMP] ============================================================")
	}
	return &contextKeys{context: context, send: send, recv: recv}, nil
}

// deriveMore derives the pending candidates in the background, one at a
// time (each scrypt run takes 32 MiB).
func (r *keyRing) deriveMore() {
	r.mu.Lock()
	if r.deriving || len(r.pending) == 0 {
		r.mu.Unlock()
		return
	}
	r.deriving = true
	r.mu.Unlock()
	utils.SafeGo("crypto.deriveAlternates", func() {
		defer func() {
			r.mu.Lock()
			r.deriving = false
			r.mu.Unlock()
		}()
		for {
			r.mu.Lock()
			if len(r.pending) == 0 {
				r.mu.Unlock()
				return
			}
			c := r.pending[0]
			r.pending = r.pending[1:]
			r.mu.Unlock()
			k, err := r.derive(c)
			if err != nil {
				utils.Debugf("[CRYPTO] derive alternate context %q: %v", c, err)
				continue
			}
			r.mu.Lock()
			r.keys = append(r.keys, k)
			n := len(r.keys)
			r.mu.Unlock()
			utils.Debugf("[CRYPTO] side=%s alternate KDF context #%d ready: %q", r.side, n-1, c)
		}
	})
}

// sendKeys picks the keys to send under. A client that has not heard the
// peer under any context moves to the next candidate every
// contextRotateEvery; an exit answers under the context the client last
// used.
func (r *keyRing) sendKeys() *contextKeys {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	if !r.exit {
		if r.locked && now.Sub(r.heard) > contextUnlockAfter {
			r.locked = false
			r.started, r.rotated = time.Time{}, time.Time{}
			utils.Debugf("[CRYPTO] side=%s peer silent for %v: will try the other KDF contexts again", r.side, contextUnlockAfter)
		}
		if !r.locked && (len(r.keys) > 1 || len(r.pending) > 0) {
			if r.started.IsZero() {
				r.started, r.rotated = now, now
			} else if now.Sub(r.rotated) >= contextRotateEvery && now.Sub(r.started) >= contextFirstRotate {
				r.rotated = now
				if len(r.pending) > 0 && !r.deriving {
					go r.deriveMore()
				}
				if len(r.keys) > 1 {
					prev := r.send
					r.send = (r.send + 1) % len(r.keys)
					if utils.Throttled("crypto.rotate."+r.side, 10*time.Second) {
						utils.Infof("[CRYPTO] no answer from the peer under KDF context %q for %v; trying %q (%d/%d)",
							r.keys[prev].context, contextRotateEvery, r.keys[r.send].context, r.send+1, len(r.keys)+len(r.pending))
					}
				}
			}
		}
	}
	return r.keys[r.send]
}

// open tries every derived context, the one in use first. It reports the
// plaintext and the index of the context that opened the packet.
func (r *keyRing) open(nonce, ciphertext, header []byte) ([]byte, int, error) {
	r.mu.Lock()
	keys := append([]*contextKeys(nil), r.keys...)
	first := r.send
	pending := len(r.pending) > 0
	r.mu.Unlock()

	plaintext, err := keys[first].recv.Open(nil, nonce, ciphertext, header)
	if err == nil {
		return plaintext, first, nil
	}
	for i, k := range keys {
		if i == first {
			continue
		}
		if p, e := k.recv.Open(nil, nonce, ciphertext, header); e == nil {
			return p, i, nil
		}
	}
	if pending {
		r.deriveMore()
	}
	return nil, -1, err
}

// accepted records that the peer spoke under keys[idx].
func (r *keyRing) accepted(idx int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.heard = time.Now()
	r.failures = 0
	if idx == r.send && (r.locked || r.exit) {
		return
	}
	prev := r.keys[r.send].context
	r.send = idx
	r.locked = true
	ctx := r.keys[idx].context
	switch {
	case prev == ctx:
		utils.Debugf("[CRYPTO] side=%s peer confirmed KDF context %q", r.side, ctx)
	case idx == 0:
		utils.Debugf("[CRYPTO] side=%s peer is back on the primary KDF context %q", r.side, ctx)
	case utils.Throttled("crypto.adopt."+r.side, 10*time.Second):
		utils.Infof("[CRYPTO] peer derives its keys from KDF context %q, not %q as this side does: switched to it (both sides should be updated to the same core)",
			ctx, r.keys[0].context)
	}
}

// failed counts a packet no candidate context could open and explains the
// likely cause now and then.
func (r *keyRing) failed() {
	r.mu.Lock()
	r.failures++
	n := r.failures
	tried := len(r.keys)
	pending := len(r.pending)
	primary := r.keys[0].context
	heard := !r.heard.IsZero()
	r.mu.Unlock()
	if n < 3 || pending > 0 || !utils.Throttled("crypto.fail."+r.side, 30*time.Second) {
		return
	}
	if heard {
		utils.Infof("[CRYPTO] %d packets from the peer failed authentication after it had worked: another device with a different key, or old data replayed on the carrier", n)
		return
	}
	utils.Infof("[CRYPTO] %d packets from the peer failed authentication under all %d KDF contexts tried (primary %q, sha256 %s): the encryption key differs from the peer's",
		n, tried, primary, utils.Sha256Short([]byte(primary)))
}

func deriveDirectionalKey(master []byte, label string) []byte {
	mac := hmac.New(sha256.New, master)
	_, _ = mac.Write([]byte("OpenFlux direction v1\x00" + label))
	return mac.Sum(nil)
}

func newGCM(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create AES cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create AES-GCM: %w", err)
	}
	return aead, nil
}

func (e *EncryptedTransport) Send(data []byte) error {
	r := e.ring
	keys := r.sendKeys()
	header := []byte{encryptedMagic[0], encryptedMagic[1], encryptedMagic[2], encryptedVersion, r.sendDir}
	nonce := make([]byte, keys.send.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		utils.Debugf("[CRYPTO] Send: rand nonce failed: %v", err)
		e.sendErr.Add(1)
		return fmt.Errorf("create packet nonce: %w", err)
	}
	packet := make([]byte, 0, len(header)+len(nonce)+len(data)+keys.send.Overhead())
	packet = append(packet, header...)
	packet = append(packet, nonce...)
	packet = keys.send.Seal(packet, nonce, data, header)

	utils.Debugf("[CRYPTO] Send #%d dir=%d plaintext=%d ciphertext=%d nonce=%s ctx=%s",
		e.sendOK.Load()+1, r.sendDir, len(data), len(packet), hex.EncodeToString(nonce), utils.Sha256Short([]byte(keys.context)))
	// Plaintext frames can carry control messages with cookie jars, so
	// they are dumped only with --sensitive; ciphertext is what the
	// carrier sees anyway.
	if utils.IsVerbose() {
		if utils.Sensitive() {
			utils.Debugf("[CRYPTO] Send plaintext hexdump:\n%s", hex.Dump(data))
		}
		utils.Debugf("[CRYPTO] Send ciphertext hexdump:\n%s", hex.Dump(packet))
	}

	err := e.Transport.Send(packet)
	if err != nil {
		e.sendErr.Add(1)
		utils.Debugf("[CRYPTO] Send FAILED: %v", err)
		return err
	}
	e.sendOK.Add(1)
	return nil
}

func (e *EncryptedTransport) Receive(callback func([]byte)) {
	r := e.ring
	e.Transport.Receive(func(packet []byte) {
		utils.Debugf("[CRYPTO] Recv raw %d ciphertext bytes", len(packet))
		if utils.IsVerbose() {
			utils.Debugf("[CRYPTO] Recv raw hexdump:\n%s", hex.Dump(packet))
		}

		const nonceSize, overhead = 12, 16 // AES-GCM
		minLen := encryptedHeader + nonceSize + overhead
		if len(packet) < minLen {
			e.recvBadLen.Add(1)
			utils.Debugf("[CRYPTO] Recv DROP: too short (%d < %d)", len(packet), minLen)
			return
		}
		header := packet[:encryptedHeader]
		if header[0] != encryptedMagic[0] || header[1] != encryptedMagic[1] ||
			header[2] != encryptedMagic[2] {
			e.recvBadHdr.Add(1)
			utils.Debugf("[CRYPTO] Recv DROP: bad magic %x (want %x)%s",
				header[:3], encryptedMagic[:], frameHint(packet))
			return
		}
		if header[3] != encryptedVersion {
			e.recvBadHdr.Add(1)
			utils.Debugf("[CRYPTO] Recv DROP: bad version %d (want %d)",
				header[3], encryptedVersion)
			return
		}
		if header[4] != r.recvDir {
			e.recvBadHdr.Add(1)
			utils.Debugf("[CRYPTO] Recv DROP: wrong direction %d (want %d): a packet of this side's own role (echo, or both peers configured as %s)",
				header[4], r.recvDir, map[bool]string{true: "exit", false: "client"}[r.exit])
			return
		}
		nonceEnd := encryptedHeader + nonceSize
		nonce := packet[encryptedHeader:nonceEnd]
		utils.Debugf("[CRYPTO] Recv #%d dir=%d nonce=%s cipherLen=%d -> decrypting",
			e.recvOK.Load()+e.recvFail.Load()+1, header[4], hex.EncodeToString(nonce), len(packet)-nonceEnd)
		plaintext, idx, err := r.open(nonce, packet[nonceEnd:], header)
		if err != nil {
			e.recvFail.Add(1)
			utils.Debugf("[CRYPTO] Recv DECRYPT FAIL dir=%d nonce=%s err=%v (recvFail=%d recvOK=%d)",
				header[4], hex.EncodeToString(nonce), err, e.recvFail.Load(), e.recvOK.Load())
			if utils.IsVerbose() {
				utils.Debugf("[CRYPTO] failed ciphertext hexdump:\n%s", hex.Dump(packet))
			}
			r.failed()
			return
		}
		if !e.rememberNonce(nonce) {
			e.recvReplay.Add(1)
			utils.Debugf("[CRYPTO] Recv REPLAY: nonce %s already seen (recvReplay=%d)",
				hex.EncodeToString(nonce), e.recvReplay.Load())
			return
		}
		r.accepted(idx)
		e.recvOK.Add(1)
		utils.Debugf("[CRYPTO] Recv DECRYPT OK #%d dir=%d plaintext=%d bytes nonce=%s ctx#%d",
			e.recvOK.Load(), header[4], len(plaintext), hex.EncodeToString(nonce), idx)
		if utils.IsVerbose() && utils.Sensitive() {
			utils.Debugf("[CRYPTO] Recv plaintext hexdump:\n%s", hex.Dump(plaintext))
		}
		callback(plaintext)
	})
}

// frameHint names what a frame that is not an encrypted record looks like,
// for the drop log: most often the peer runs the other layering (classic vs
// Session) or has no encryption at all.
func frameHint(p []byte) string {
	if len(p) == 0 {
		return ""
	}
	switch p[0] {
	case batchFormatVersion:
		return " - a batch-v2 frame: the peer runs classic mode (codec outside encryption)"
	case 0x00, CompressionMarker:
		return " - a legacy per-packet frame: the peer runs classic mode with --codec=legacy"
	}
	if p[0]>>4 == 4 {
		return " - a bare IPv4 packet: the peer runs without encryption"
	}
	return ""
}

func (e *EncryptedTransport) rememberNonce(nonce []byte) bool {
	key := string(nonce)
	e.seenMu.Lock()
	defer e.seenMu.Unlock()
	if _, exists := e.seen[key]; exists {
		return false
	}
	e.seen[key] = struct{}{}
	e.seenOrder = append(e.seenOrder, key)
	if len(e.seenOrder) > maxSeenNonces {
		oldest := e.seenOrder[0]
		e.seenOrder = e.seenOrder[1:]
		delete(e.seen, oldest)
	}
	return true
}

// CryptoStats returns the diagnostic counters.
func (e *EncryptedTransport) CryptoStats() (sendOK, sendErr, recvOK, recvFail, recvReplay, recvBadHdr, recvBadLen uint64) {
	return e.sendOK.Load(), e.sendErr.Load(), e.recvOK.Load(), e.recvFail.Load(),
		e.recvReplay.Load(), e.recvBadHdr.Load(), e.recvBadLen.Load()
}

// minInt is defined in session.go; declared here only if that file is
// somehow compiled out. Keep a local guard so this file stands alone.
var _ = minInt
