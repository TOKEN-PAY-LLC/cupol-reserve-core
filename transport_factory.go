package main

import (
	"fmt"
	"strconv"

	"openflux/transport"
	"openflux/transport/control"
	"openflux/transport/manager"
	"openflux/transport/oneme"
	"openflux/transport/script"
	"openflux/transport/yandex"
)

// yandexCookiesFile is --yandex-cookies-file: a Netscape cookies.txt with
// a Yandex login that every vyandex transport starts with.
var yandexCookiesFile string

func newVolgaTransport(docURL string, cfg transport.TransportConfig) (transport.Transport, error) {
	t := yandex.NewYandexVolgaTransport(docURL, cfg)
	if yandexCookiesFile != "" {
		if err := t.LoadCookieFile(yandexCookiesFile); err != nil {
			return nil, err
		}
	}
	return t, nil
}

// transportFactory builds a raw transport from a control.TransportConfig.
// It is the single place that knows every transport package. main.go passes
// it into manager.New, and manager calls it whenever the peer asks the exit
// to bring up an additional transport at runtime.
func transportFactory(baseCfg transport.TransportConfig) manager.Factory {
	return func(cfg *control.TransportConfig) (transport.Transport, error) {
		if cfg == nil {
			return nil, fmt.Errorf("factory: nil config")
		}
		switch cfg.Type {
		case "yandex":
			return yandex.NewYandexDocsTransport(cfg.URL, baseCfg), nil
		case "vyandex":
			return newVolgaTransport(cfg.URL, baseCfg)
		case "script":
			// path: either a plain .js file (its detached path+".sig" must
			// verify) or a .flux package (script+manifest+icon signed
			// together - see transport/script/flux.go), picked by extension.
			// pubkey: hex ed25519 public key it must be signed with.
			// name: optional, defaults to the script's own info().name for
			// logging - the factory doesn't know that until Start().
			scriptPath, _ := cfg.Params["path"].(string)
			pubkeyHex, _ := cfg.Params["pubkey"].(string)
			name, _ := cfg.Params["name"].(string)
			if name == "" {
				name = "script"
			}
			if scriptPath == "" {
				return nil, fmt.Errorf("factory: script transport missing \"path\" param")
			}
			pubKey, err := script.DecodePublicKeyHex(pubkeyHex)
			if err != nil {
				return nil, fmt.Errorf("factory: script transport: %w", err)
			}
			return script.New(name, scriptPath, pubKey, cfg.URL, cfg.Params, baseCfg)
		case "oneme":
			token, _ := cfg.Params["token"].(string)
			uidStr, _ := cfg.Params["uid"].(string)
			uid, _ := strconv.ParseInt(uidStr, 10, 64)
			exit, _ := cfg.Params["exit"].(bool)
			return oneme.NewOneMeTransport(exit, token, uid, baseCfg), nil
		case "direct":
			dcfg := transport.DefaultDirectConfig()
			if v, ok := cfg.Params["listen"].(string); ok {
				dcfg.ListenAddr = v
			}
			if v, ok := cfg.Params["dial"].(string); ok {
				dcfg.DialAddr = v
			}
			if v, ok := cfg.Params["is_exit"].(bool); ok {
				dcfg.IsExit = v
			}
			return transport.NewDirectTransport(baseCfg, dcfg), nil
		default:
			return nil, fmt.Errorf("factory: unknown transport type %q", cfg.Type)
		}
	}
}
