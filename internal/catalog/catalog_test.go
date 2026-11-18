package catalog

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/singularityos-lab/sinty-devices/profile"
)

const doc = `{
  "schema": 1,
  "id": "arduino/uno-q",
  "vendor": "Arduino",
  "model": "UNO Q",
  "arch": "aarch64",
  "min_flash_version": "0.2.0",
  "match": {"compatible": ["arduino,imola"]},
  "install": {"method": "installer-stick", "esp": "efi", "replace": ["rootfs", "userdata"]}
}`

func server(t *testing.T, files map[string]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := files[strings.TrimPrefix(r.URL.Path, "/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func index(t *testing.T, digest string) string {
	t.Helper()
	ix := &profile.Index{Schema: 1, Devices: []profile.Entry{{
		ID: "arduino/uno-q", Path: "devices/arduino/uno-q.json", Vendor: "Arduino", Model: "UNO Q",
		MinFlashVersion: "0.2.0", SHA256: digest,
	}}}
	b, err := ix.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestProfileVersionGate(t *testing.T) {
	srv := server(t, map[string]string{
		"index.json":                 index(t, profile.Digest([]byte(doc))),
		"devices/arduino/uno-q.json": doc,
	})
	c := New(srv.URL, t.TempDir())
	ix, err := c.Index(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var old *TooOldError
	if _, err := c.Profile(context.Background(), ix, "arduino/uno-q", profile.Version{0, 1, 9}, false); !errors.As(err, &old) {
		t.Fatalf("old tool accepted: %v", err)
	}
	p, err := c.Profile(context.Background(), ix, "arduino/uno-q", profile.Version{0, 2, 0}, false)
	if err != nil || p.Model != "UNO Q" {
		t.Fatalf("p=%v err=%v", p, err)
	}
	if _, err := c.Profile(context.Background(), ix, "arduino/uno-q", profile.Version{}, true); err != nil {
		t.Fatalf("development build refused: %v", err)
	}
}

func TestProfileDigestMismatch(t *testing.T) {
	srv := server(t, map[string]string{
		"index.json":                 index(t, strings.Repeat("0", 64)),
		"devices/arduino/uno-q.json": doc,
	})
	c := New(srv.URL, "")
	ix, err := c.Index(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Profile(context.Background(), ix, "arduino/uno-q", profile.Version{9, 9, 9}, false); err == nil {
		t.Fatal("tampered profile accepted")
	}
}

func TestCacheFallback(t *testing.T) {
	files := map[string]string{
		"index.json":                 index(t, profile.Digest([]byte(doc))),
		"devices/arduino/uno-q.json": doc,
	}
	srv := server(t, files)
	cache := t.TempDir()
	c := New(srv.URL, cache)
	if _, err := c.Index(context.Background()); err != nil {
		t.Fatal(err)
	}
	srv.Close()
	if _, err := c.Index(context.Background()); err != nil {
		t.Fatalf("offline index not served from cache: %v", err)
	}
}

func TestUnsafePath(t *testing.T) {
	for _, p := range []string{"/etc/passwd", "../x.json", "devices/../../x", "index.json", ""} {
		if safePath(p) {
			t.Errorf("%q accepted", p)
		}
	}
	if !safePath("devices/arduino/uno-q.json") {
		t.Error("valid path refused")
	}
}
