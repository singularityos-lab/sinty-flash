package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestSeedBridgeTrustsTheHostAndNamesTheDeviceKey(t *testing.T) {
	dir := t.TempDir()
	_, hostCert, _, err := bridgeIdentity()
	if err != nil {
		t.Fatal(err)
	}
	certPath := filepath.Join(dir, "host.crt")
	if err := os.WriteFile(certPath, hostCert, 0o644); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(dir, "data")
	device, err := seedBridge(data, certPath)
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(data, bridgeState)
	if _, err := os.Stat(filepath.Join(state, "enabled")); err != nil {
		t.Fatal("bridge not enabled:", err)
	}
	got, err := certFingerprint(filepath.Join(state, "device.crt"))
	if err != nil || got != device {
		t.Fatalf("device key %s, certificate says %s (%v)", device, got, err)
	}
	raw, err := os.ReadFile(filepath.Join(state, "keystore.json"))
	if err != nil {
		t.Fatal(err)
	}
	var ks bridgeKeystore
	if err := json.Unmarshal(raw, &ks); err != nil {
		t.Fatal(err)
	}
	host, _ := certFingerprint(certPath)
	if ks.Version != 1 || len(ks.Hosts) != 1 || ks.Hosts[0].Fingerprint != host || ks.Hosts[0].Label == "" {
		t.Fatalf("keystore %+v does not trust host %s", ks, host)
	}
	if fi, _ := os.Stat(filepath.Join(state, "device.key")); fi.Mode().Perm() != 0o600 {
		t.Fatalf("device key mode %v", fi.Mode().Perm())
	}
}
