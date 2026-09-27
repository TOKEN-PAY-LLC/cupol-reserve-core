package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseTransportListScriptReference(t *testing.T) {
	specs, err := parseTransportList("direct:100,script:mailru:30,script:boards")
	if err != nil {
		t.Fatalf("parseTransportList: %v", err)
	}
	if len(specs) != 3 {
		t.Fatalf("got %d specs, want 3", len(specs))
	}
	if specs[0].Type != "direct" || specs[0].Priority != 100 {
		t.Fatalf("specs[0] = %+v", specs[0])
	}
	if specs[1].Type != "script:mailru" || specs[1].Priority != 30 {
		t.Fatalf("specs[1] = %+v, want Type=script:mailru Priority=30", specs[1])
	}
	// No explicit priority - the whole string is the type, default priority.
	if specs[2].Type != "script:boards" || specs[2].Priority != 50 {
		t.Fatalf("specs[2] = %+v, want Type=script:boards Priority=50", specs[2])
	}
}

func TestExpandScriptTypeRequiresDirAndPubkey(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "mailru.js"), []byte("//"), 0o644); err != nil {
		t.Fatalf("seed mailru.js: %v", err)
	}
	spec := transportSpec{Name: "script:mailru", Type: "script:mailru", Priority: 30}

	if _, err := expandScriptType(spec, "", "somekeyhex"); err == nil {
		t.Fatal("expected an error with no --script-dir")
	}
	if _, err := expandScriptType(spec, dir, ""); err == nil {
		t.Fatal("expected an error with no --script-pubkey")
	}

	got, err := expandScriptType(spec, dir, "somekeyhex")
	if err != nil {
		t.Fatalf("expandScriptType: %v", err)
	}
	if got.Type != "script" {
		t.Fatalf("Type = %q, want %q", got.Type, "script")
	}
	if got.Name != "mailru" {
		t.Fatalf("Name = %q, want %q", got.Name, "mailru")
	}
	if want := filepath.Join(dir, "mailru.js"); got.Params["path"] != want {
		t.Fatalf("Params[path] = %v, want %v", got.Params["path"], want)
	}
	if got.Params["pubkey"] != "somekeyhex" {
		t.Fatalf("Params[pubkey] = %v, want somekeyhex", got.Params["pubkey"])
	}
	if got.Params["name"] != "mailru" {
		t.Fatalf("Params[name] = %v, want mailru", got.Params["name"])
	}
}

func TestExpandScriptTypeMissingScriptErrors(t *testing.T) {
	dir := t.TempDir() // empty - neither mailru.flux nor mailru.js exists
	spec := transportSpec{Name: "script:mailru", Type: "script:mailru", Priority: 30}
	if _, err := expandScriptType(spec, dir, "somekeyhex"); err == nil {
		t.Fatal("expected an error when neither <name>.flux nor <name>.js exists in --script-dir")
	}
}

func TestExpandScriptTypePrefersFluxOverJS(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "mailru.js"), []byte("//"), 0o644); err != nil {
		t.Fatalf("seed mailru.js: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "mailru.flux"), []byte("PK"), 0o644); err != nil {
		t.Fatalf("seed mailru.flux: %v", err)
	}
	spec := transportSpec{Name: "script:mailru", Type: "script:mailru", Priority: 30}
	got, err := expandScriptType(spec, dir, "somekeyhex")
	if err != nil {
		t.Fatalf("expandScriptType: %v", err)
	}
	if want := filepath.Join(dir, "mailru.flux"); got.Params["path"] != want {
		t.Fatalf("Params[path] = %v, want %v (a .flux package should win over a plain .js)", got.Params["path"], want)
	}
}

func TestExpandScriptTypeIgnoresNonScriptSpecs(t *testing.T) {
	spec := transportSpec{Name: "mailru", Type: "mailru", Priority: 50}
	got, err := expandScriptType(spec, "", "")
	if err != nil {
		t.Fatalf("expandScriptType: %v", err)
	}
	if got.Name != spec.Name || got.Type != spec.Type || got.Priority != spec.Priority || got.Params != nil {
		t.Fatalf("non-script spec was modified: got %+v, want %+v", got, spec)
	}
}
