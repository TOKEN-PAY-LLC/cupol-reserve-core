package transport

import (
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/p1neappleXpress/OpenFlux/transport/control"
)

// Leave enough time for the batch writer and Windows scheduler; the production
// settings remain 10-second probes and a 30-second peer timeout.
func recoveryKeepalive(sessions ...*Session) {
	for _, s := range sessions {
		s.keepaliveInterval = 50 * time.Millisecond
		s.linkTimeout = 750 * time.Millisecond
	}
}

func restoreWires(wires ...*startCountingWire) {
	for _, w := range wires {
		w.mu.Lock()
		w.drop = false
		w.mu.Unlock()
	}
}

func sessionID(s *Session) [32]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.local
}

func TestSessionWaitsForExitConfirmation(t *testing.T) {
	client, exit, cw, _ := linkedSessions(t, "direct")
	startPair(t, client, exit)
	// Hold back the client's challenge echo. The old exit is still connected,
	// but it has not accepted the client's replacement session yet.
	blackhole(cw["direct"])
	client.mu.Lock()
	client.resetLocked()
	local := client.local
	client.mu.Unlock()
	var candidate [32]byte
	candidate[0] = 42
	offer := &control.Envelope{
		Kind: control.KindHello, Role: control.RoleExit, Local: candidate, Peer: local,
		Hello: &control.HelloTail{
			Capabilities:  control.Capabilities(testParams.Capabilities),
			MaxPacketSize: uint16(testParams.MaxPacketSize),
		},
	}
	client.receiveHello(client.links["direct"], offer)
	if client.IsConnected() || client.ActiveTransport() != "" {
		t.Fatal("a replacement challenge was reported as an established connection before the exit confirmed it")
	}
	if err := client.Send(testIPv4(40, 6)); !errors.Is(err, ErrNegotiationPending) {
		t.Fatalf("unconfirmed replacement Send = %v, want negotiation pending", err)
	}
	offer.Hello.Ready = 1
	client.receiveHello(client.links["direct"], offer)
	if !client.IsConnected() || client.ActiveTransport() != "direct" {
		t.Fatal("the confirmed replacement did not become available")
	}
}

func TestSessionRestoresPreferredCarrierWithoutRenegotiation(t *testing.T) {
	client, exit, cw, ew := linkedSessions(t, "direct", "yandex")
	recoveryKeepalive(client, exit)
	var atExit, atClient atomic.Int32
	exit.Receive(func([]byte) { atExit.Add(1) })
	client.Receive(func([]byte) { atClient.Add(1) })
	startPair(t, client, exit)
	eventually(t, "both carriers to reach the peer", func() bool {
		return len(client.LiveTransports()) == 2 && len(exit.LiveTransports()) == 2
	})
	clientID, exitID := sessionID(client), sessionID(exit)

	blackhole(cw["direct"], ew["direct"])
	eventually(t, "both peers to select the reserve carrier", func() bool {
		return client.ActiveTransport() == "yandex" && exit.ActiveTransport() == "yandex"
	})
	if !client.IsConnected() || !exit.IsConnected() {
		t.Fatal("a healthy reserve must keep both peers connected")
	}
	if err := client.Send(testIPv4(40, 6)); err != nil {
		t.Fatal(err)
	}
	if err := exit.Send(testIPv4(40, 6)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "bidirectional reserve delivery", func() bool {
		return atExit.Load() == 1 && atClient.Load() == 1
	})

	restoreWires(cw["direct"], ew["direct"])
	eventually(t, "the preferred carrier to recover", func() bool {
		return client.ActiveTransport() == "direct" && exit.ActiveTransport() == "direct"
	})
	if err := client.Send(testIPv4(40, 6)); err != nil {
		t.Fatal(err)
	}
	if err := exit.Send(testIPv4(40, 6)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "bidirectional delivery after recovery", func() bool {
		return atExit.Load() == 2 && atClient.Load() == 2
	})
	if sessionID(client) != clientID || sessionID(exit) != exitID {
		t.Fatal("carrier failover or recovery replaced the logical session")
	}
}

func TestSessionReportsTotalOutageAndRecovers(t *testing.T) {
	client, exit, cw, ew := linkedSessions(t, "direct", "yandex")
	recoveryKeepalive(client, exit)
	var got atomic.Int32
	exit.Receive(func([]byte) { got.Add(1) })
	startPair(t, client, exit)
	eventually(t, "keepalive support to be detected", func() bool {
		client.mu.Lock()
		defer client.mu.Unlock()
		return client.peerKeepalive
	})
	blackhole(cw["direct"], ew["direct"], cw["yandex"], ew["yandex"])
	eventually(t, "a total peer outage to be reported", func() bool {
		return !client.IsConnected() && client.ActiveTransport() == ""
	})
	if err := client.Send(testIPv4(40, 6)); err == nil {
		t.Fatal("an unnegotiated session accepted traffic during a total outage")
	}
	restoreWires(cw["yandex"], ew["yandex"])
	eventually(t, "reconnection over the reserve carrier", func() bool {
		return client.IsConnected() && exit.IsConnected() && client.ActiveTransport() == "yandex"
	})
	if err := client.Send(testIPv4(40, 6)); err != nil {
		t.Fatal(err)
	}
	eventually(t, "data delivery after a total outage", func() bool { return got.Load() == 1 })
}
