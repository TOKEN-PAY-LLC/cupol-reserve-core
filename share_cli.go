package main

import (
	"fmt"
	"log"
	"net"
	"os"
	"strings"

	"universal-bypass-tool/share"
)

// emitShareLink печатает openflux://-ссылку и QR для текущей конфигурации.
//
// Формат — общий стандарт из пакета share, тот же, что у Android и CLI
// upstream, поэтому ссылку можно отсканировать любым клиентом OpenFlux.
//
// В ссылке едет секрет шифрования: кто её увидел, тот может подключиться к
// узлу. Обращаться как с файлом ключа.
func emitShareLink(transportType, docURL, directAddr, shareHost, keyFile string) {
	secret := ""
	if keyFile != "" {
		b, err := os.ReadFile(keyFile)
		if err != nil {
			log.Fatalf("--share: read key file: %v", err)
		}
		secret = strings.TrimSpace(string(b))
	}

	t := share.Transport{Type: transportType}
	cfg := share.Config{Secret: secret}

	if name, err := os.Hostname(); err == nil && name != "" {
		cfg.Name = "OpenFlux " + name
	} else {
		cfg.Name = "OpenFlux"
	}

	switch transportType {
	case "direct":
		// Клиент должен набрать публичный адрес узла, а --direct-addr на узле
		// это адрес прослушивания (обычно 0.0.0.0) — из него годится только порт.
		_, port, err := net.SplitHostPort(directAddr)
		if err != nil {
			log.Fatalf("--share: --direct-addr must be host:port, got %q", directAddr)
		}
		if shareHost == "" {
			log.Fatalf("--share: --transport=direct needs --share-host (the address clients dial)")
		}
		t.Dial = net.JoinHostPort(shareHost, port)
		// Стандарт требует для direct согласованную сессию.
		cfg.Negotiate = true
		// Контекст пишем явно — тот же, что выведет pickSessionContext: у узла
		// без документа это плейсхолдер. Так ключ сойдётся и с нашим клиентом,
		// и с Android.
		cfg.Context = contextPlaceholder
	case "oneme":
		log.Fatalf("--share: MAX is not shareable — its token belongs to this account, clients need their own")
	default:
		if docURL == "" || docURL == "http://#" {
			log.Fatalf("--share: --url is required for transport %q", transportType)
		}
		t.URL = docURL
		if secret != "" {
			cfg.Context = docURL
		}
	}
	cfg.Transports = []share.Transport{t}

	link, err := share.Encode(cfg)
	if err != nil {
		log.Fatalf("--share: %v", err)
	}
	qr, err := share.Terminal(link)
	if err != nil {
		log.Fatalf("--share: render QR: %v", err)
	}
	fmt.Println(qr)
	fmt.Println(link)
	if secret != "" {
		fmt.Fprintln(os.Stderr, "\n!! Ссылка содержит секрет шифрования — обращайтесь с ней как с файлом ключа.")
	}
}
