package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"time"
)

const bridgeState = "lib/sinty-sdb"

type bridgeHost struct {
	Label       string    `json:"label"`
	Fingerprint string    `json:"fingerprint"`
	PairedAt    time.Time `json:"paired_at"`
	LastUsed    time.Time `json:"last_used"`
}

type bridgeKeystore struct {
	Version int          `json:"version"`
	Hosts   []bridgeHost `json:"hosts"`
}

func parseTrustHost(name string, args []string) (string, []string, error) {
	fl := flag.NewFlagSet(name, flag.ContinueOnError)
	fl.SetOutput(io.Discard)
	host := fl.String("trust-host", "", "")
	if err := fl.Parse(args); err != nil {
		return "", nil, err
	}
	return *host, fl.Args(), nil
}

func certFingerprint(path string) (string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	block, _ := pem.Decode(data)
	if block == nil || block.Type != "CERTIFICATE" {
		return "", fmt.Errorf("%s is not a PEM certificate", path)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return "", fmt.Errorf("%s: %w", path, err)
	}
	return spkiFingerprint(cert.RawSubjectPublicKeyInfo), nil
}

func spkiFingerprint(spki []byte) string {
	sum := sha256.Sum256(spki)
	return hex.EncodeToString(sum[:])
}

func seedBridge(data, hostCert string) (string, error) {
	if hostCert == "" {
		return "", errors.New("no host certificate to trust")
	}
	host, err := certFingerprint(hostCert)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(data, bridgeState)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	keyPEM, certPEM, device, err := bridgeIdentity()
	if err != nil {
		return "", err
	}
	now := time.Now().UTC()
	store, err := json.MarshalIndent(bridgeKeystore{Version: 1, Hosts: []bridgeHost{{Label: "flash-host", Fingerprint: host, PairedAt: now, LastUsed: now}}}, "", "  ")
	if err != nil {
		return "", err
	}
	for _, f := range []struct {
		name string
		data []byte
		mode os.FileMode
	}{
		{"device.key", keyPEM, 0o600},
		{"device.crt", certPEM, 0o644},
		{"keystore.json", append(store, '\n'), 0o600},
		{"enabled", nil, 0o644},
	} {
		if err := os.WriteFile(filepath.Join(dir, f.name), f.data, f.mode); err != nil {
			return "", err
		}
	}
	return device, nil
}

func bridgeIdentity() (keyPEM, certPEM []byte, fingerprint string, err error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, "", err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, "", err
	}
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "device"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(20, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	if err != nil {
		return nil, nil, "", err
	}
	privDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, nil, "", err
	}
	spki, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		return nil, nil, "", err
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privDER})
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return keyPEM, certPEM, spkiFingerprint(spki), nil
}

func printBridge(device string) {
	if device != "" {
		fmt.Printf("debug bridge on from the first boot, trusting this host; device key %s\n", device)
	}
}
