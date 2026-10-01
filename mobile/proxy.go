// Proxy mode exposes a local SOCKS5 listener backed by the same encrypted
// document transport as the tunnel packet mode, but routed through an
// in-process gVisor TCP/IP stack (tunnel.TCPTunnel) instead of an Android
// VpnService TUN.
// This mirrors exactly what the desktop CLI's client mode already does in
// main.go, so it needs no changes on the exit node / VDS side.
package mobile

import (
	"fmt"
	"strings"
	"sync"

	"github.com/p1neappleXpress/OpenFlux/socks5"
	"github.com/p1neappleXpress/OpenFlux/transport"
	"github.com/p1neappleXpress/OpenFlux/tunnel"
	"github.com/p1neappleXpress/OpenFlux/utils"
)

var proxy = proxyState{}

type proxyState struct {
	mu        sync.Mutex
	running   bool
	transport transport.Transport
	tun       *tunnel.TCPTunnel
	servers   []*socks5.SOCKS5Server
}

// StartProxy launches the local SOCKS5 proxy in classic single-transport
// mode. Returns "" once the listener is bound and the transport handshake
// has started, or a user-readable error. Call ProxyIsConnected to learn when
// the tunnel itself is actually up. Hostname lookups are resolved locally by
// TCPTunnel (the same as the desktop CLI client), so no exit-node changes are
// required. listenAddr can also be "127.0.0.1:1080|192.168.43.1:1080":
// the first listener remains local and the second requires username/password.
// This avoids exposing an unauthenticated proxy to every network interface.
// With a single address, username/password applies to that listener.
// bypassDomains is a newline-separated list (from the
// Android "Маршрутизация" settings tab) of domains to dial directly instead
// of through the tunnel; pass "" for none.
func StartProxy(transportType, documentURL, encryptionSecret, codec, maxToken, maxUid, listenAddr, username, password, bypassDomains string) string {
	if msg := validateClassic(transportType, documentURL, encryptionSecret); msg != "" {
		return msg
	}
	return startProxyWith(func() (transport.Transport, error) {
		return classicTransport(transportType, documentURL, encryptionSecret, codec, maxToken, maxUid, false)
	}, listenAddr, username, password, bypassDomains)
}

// StartSessionProxy is StartProxy in Session mode, see StartSession.
func StartSessionProxy(specsJSON, encryptionSecret, listenAddr, username, password, bypassDomains string) string {
	return startProxyWith(func() (transport.Transport, error) {
		return buildSession(specsJSON, encryptionSecret, false)
	}, listenAddr, username, password, bypassDomains)
}

func startProxyWith(build func() (transport.Transport, error), listenAddr, username, password, bypassDomains string) string {
	proxy.mu.Lock()
	if proxy.running {
		proxy.mu.Unlock()
		return ""
	}
	proxy.mu.Unlock()

	utils.SetLevel(int(debugLevel.Load()))
	utils.SetLogSink(appendLog)
	appendLog("[ANDROID] Запуск прокси-транспорта")

	trans, err := build()
	if err != nil {
		appendLog(fmt.Sprintf("[ERROR] Ошибка запуска прокси: %v", err))
		detachCaptcha()
		setAuthProxy(nil)
		return err.Error()
	}
	if err := trans.Start(); err != nil {
		appendLog(fmt.Sprintf("[ERROR] Ошибка запуска прокси: %v", err))
		detachCaptcha()
		setAuthProxy(nil)
		return err.Error()
	}

	tun := tunnel.NewTCPTunnel(trans, false)
	if err := tun.UseRemoteDNS("1.1.1.1:53"); err != nil {
		tun.Close()
		_ = trans.Stop()
		detachCaptcha()
		setAuthProxy(nil)
		return err.Error()
	}
	var dialer socks5.Dialer = tun
	if strings.TrimSpace(bypassDomains) != "" {
		dialer = newSplitDialer(tun, strings.Split(bypassDomains, "\n"))
	}
	addresses := strings.Split(listenAddr, "|")
	if len(addresses) > 2 || (len(addresses) == 2 && (username == "" || password == "")) {
		tun.Close()
		_ = trans.Stop()
		detachCaptcha()
		setAuthProxy(nil)
		return "Для сетевого SOCKS5 нужен логин и пароль"
	}
	servers := make([]*socks5.SOCKS5Server, 0, len(addresses))
	for i, address := range addresses {
		server := socks5.NewSOCKS5Server(address, dialer)
		if username != "" && (len(addresses) == 1 || i == 1) {
			server.SetAuth(username, password)
		}
		if err := server.Bind(); err != nil {
			for _, bound := range servers {
				_ = bound.Close()
			}
			tun.Close()
			_ = trans.Stop()
			appendLog(fmt.Sprintf("[ERROR] Не удалось занять %s: %v", address, err))
			detachCaptcha()
			setAuthProxy(nil)
			return fmt.Sprintf("Порт %s недоступен", address)
		}
		servers = append(servers, server)
	}

	proxy.mu.Lock()
	proxy.running = true
	proxy.transport = trans
	proxy.tun = tun
	proxy.servers = servers
	proxy.mu.Unlock()

	for _, listener := range servers {
		server := listener
		utils.SafeGo("mobile.proxyServe", func() {
			err := server.Start()
			proxy.mu.Lock()
			stillRunning := proxy.running
			proxy.mu.Unlock()
			if stillRunning && err != nil {
				appendLog(fmt.Sprintf("[ERROR] Прокси остановлен: %v", err))
			}
		})
	}

	appendLog(fmt.Sprintf("[SUCCESS] SOCKS5-прокси слушает %s", listenAddr))
	return ""
}

func StopProxy() {
	proxy.mu.Lock()
	servers := proxy.servers
	trans := proxy.transport
	proxy.running = false
	proxy.transport = nil
	proxy.tun = nil
	proxy.servers = nil
	proxy.mu.Unlock()
	detachCaptcha()
	CancelCaptcha()
	setAuthProxy(nil)
	clearRoute()
	appendLog("[ANDROID] Остановка прокси-транспорта")
	for _, server := range servers {
		_ = server.Close()
	}
	if trans != nil {
		_ = trans.Stop()
	}
}

func ProxyIsRunning() bool {
	proxy.mu.Lock()
	defer proxy.mu.Unlock()
	return proxy.running
}

func ProxyIsConnected() bool {
	proxy.mu.Lock()
	trans := proxy.transport
	proxy.mu.Unlock()
	return trans != nil && trans.IsConnected()
}

// ProxyBytesSent and ProxyBytesReceived return running totals relayed
// through the local SOCKS5 server (client -> internet and internet ->
// client respectively) across every connection since StartProxy, for a live
// speed indicator. Both are 0 if the proxy isn't running.
func ProxyBytesSent() int64 {
	proxy.mu.Lock()
	servers := proxy.servers
	proxy.mu.Unlock()
	var total int64
	for _, server := range servers {
		total += server.BytesSent()
	}
	return total
}

func ProxyBytesReceived() int64 {
	proxy.mu.Lock()
	servers := proxy.servers
	proxy.mu.Unlock()
	var total int64
	for _, server := range servers {
		total += server.BytesReceived()
	}
	return total
}
