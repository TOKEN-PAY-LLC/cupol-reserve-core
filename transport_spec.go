package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"openflux/transport"
	"openflux/transport/control"
	"openflux/transport/manager"
)

// transportSpec is one entry from --transports plus its per-type URL/params.
type transportSpec struct {
	Name     string
	Type     string
	Priority int
	URL      string
	Params   map[string]interface{}
}

// parseTransportList parses "direct:100,yandex:50,mailru:30" as well as a
// script: reference, whose OWN type carries a colon: "script:mailru:30".
// The priority, if present, is always the LAST colon-separated field - that
// stays unambiguous even for "script:mailru" (no priority: "mailru" isn't a
// number, so the whole string is kept as the type) vs "script:mailru:30"
// (30 is the last field and does parse, so it's peeled off as the
// priority). Priority is optional; default is 50.
func parseTransportList(s string) ([]transportSpec, error) {
	var out []transportSpec
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		typ := part
		prio := 50
		if i := strings.LastIndex(part, ":"); i >= 0 {
			if v, err := strconv.Atoi(part[i+1:]); err == nil {
				typ = part[:i]
				prio = v
			}
		}
		if typ == "" {
			return nil, fmt.Errorf("bad transport entry %q", part)
		}
		out = append(out, transportSpec{
			Name:     typ, // by default the name and the type match
			Type:     typ,
			Priority: prio,
		})
	}
	return out, nil
}

// expandScriptType rewrites a spec whose Type is a "script:<name>"
// reference into the plain "script" type transport_factory.go's factory
// case expects, filling Params from --script-dir/--script-pubkey. A spec
// whose Type isn't a script: reference is returned unchanged - this is a
// no-op for every native transport.
func expandScriptType(spec transportSpec, scriptDir, pubkeyHex string) (transportSpec, error) {
	name, ok := strings.CutPrefix(spec.Type, "script:")
	if !ok {
		return spec, nil
	}
	if name == "" {
		return spec, fmt.Errorf("transport %q: script: needs a name, e.g. script:mailru", spec.Type)
	}
	if scriptDir == "" {
		return spec, fmt.Errorf("transport %q: --script-dir is required for script: transports", spec.Type)
	}
	if pubkeyHex == "" {
		return spec, fmt.Errorf("transport %q: --script-pubkey is required for script: transports", spec.Type)
	}
	scriptFile, err := resolveScriptFile(scriptDir, name)
	if err != nil {
		return spec, err
	}
	params := map[string]interface{}{
		"path":   scriptFile,
		"pubkey": pubkeyHex,
		"name":   name,
	}
	// Let any params already on the spec (from a .conf [Transport] section,
	// or the oneme/direct-style --extra map) add to or override these.
	for k, v := range spec.Params {
		params[k] = v
	}
	if spec.Name == spec.Type {
		spec.Name = name
	}
	spec.Type = "script"
	spec.Params = params
	return spec, nil
}

// resolveScriptFile finds a script: transport's loadable file, preferring
// the packaged <name>.flux (script + signed manifest/icon together) and
// falling back to the plain <name>.js (+ detached .sig) every script
// shipped as before .flux existed.
func resolveScriptFile(scriptDir, name string) (string, error) {
	flux := filepath.Join(scriptDir, name+".flux")
	if _, err := os.Stat(flux); err == nil {
		return flux, nil
	}
	js := filepath.Join(scriptDir, name+".js")
	if _, err := os.Stat(js); err == nil {
		return js, nil
	}
	return "", fmt.Errorf("script %q: neither %s nor %s found in %s", name, name+".flux", name+".js", scriptDir)
}

// buildTransportSpecs assembles the config for each name in the list, using
// per-type URL flags (--yandex-url, --mailru-url, ...), global settings,
// and (for a script: entry) --script-dir/--script-pubkey.
func buildTransportSpecs(specs []transportSpec, urls map[string]string, extra map[string]map[string]interface{}, scriptDir, scriptPubkey string) ([]transportSpec, error) {
	for i := range specs {
		// Only a flag that was set overrides: the unset ones are "" and
		// would wipe the URL of a .conf [Transport] section.
		if u, ok := urls[specs[i].Type]; ok && u != "" {
			specs[i].URL = u
		}
		if p, ok := extra[specs[i].Type]; ok {
			specs[i].Params = p
		}
		expanded, err := expandScriptType(specs[i], scriptDir, scriptPubkey)
		if err != nil {
			return nil, err
		}
		specs[i] = expanded
	}
	return specs, nil
}

// makeRawTransport builds a raw transport from a spec without the Manager's
// factory (bootstrap path). The Manager's factory is only used for transports
// added later via SubtypeTransportStart.
func makeRawTransport(spec transportSpec, baseCfg transport.TransportConfig) (transport.Transport, error) {
	cfg := &control.TransportConfig{
		Name:   spec.Name,
		Type:   spec.Type,
		URL:    spec.URL,
		Params: spec.Params,
	}
	return transportFactory(baseCfg)(cfg)
}

// registerBootstrapTransports wires every spec into the manager, and also
// calls Session.AddTransport with the shared secret/context so the handshake
// can use any of them.
func registerBootstrapTransports(m *manager.Manager, specs []transportSpec, baseCfg transport.TransportConfig, secret, ctx string) error {
	for _, spec := range specs {
		raw, err := makeRawTransport(spec, baseCfg)
		if err != nil {
			return fmt.Errorf("%s: %w", spec.Name, err)
		}
		var provider manager.CookieProvider
		if p, ok := raw.(manager.CookieProvider); ok {
			provider = p
		}

		// Session-side: wrap with Encrypted+Batched and register for handshake.
		if err := m.Session().AddTransport(spec.Name, raw, secret, ctx, spec.Priority); err != nil {
			return fmt.Errorf("session add %s: %w", spec.Name, err)
		}
		// Manager-side: keep the raw pointer and its cookie provider.
		if err := m.Add(spec.Name, spec.Type, raw, spec.Priority, provider); err != nil {
			return fmt.Errorf("manager add %s: %w", spec.Name, err)
		}
	}
	return nil
}
