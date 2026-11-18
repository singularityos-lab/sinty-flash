package catalog

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/singularityos-lab/sinty-devices/profile"
)

const DefaultSource = "https://raw.githubusercontent.com/singularityos-lab/sinty-devices/main"

const maxDocument = 1 << 20

type Catalog struct {
	Source string
	Cache  string
	Client *http.Client
}

func New(source, cache string) *Catalog {
	return &Catalog{Source: source, Cache: cache, Client: &http.Client{Timeout: 30 * time.Second}}
}

type TooOldError struct {
	ID       string
	Required profile.Version
	Have     profile.Version
}

func (e *TooOldError) Error() string {
	return fmt.Sprintf("%s needs sinty-flash %s or newer, this is %s: update sinty-flash first", e.ID, e.Required, e.Have)
}

func (c *Catalog) Index(ctx context.Context) (*profile.Index, error) {
	data, err := c.fetch(ctx, "index.json")
	if err != nil {
		return nil, err
	}
	return profile.ParseIndex(data)
}

func (c *Catalog) Profile(ctx context.Context, ix *profile.Index, id string, have profile.Version, dev bool) (*profile.Profile, error) {
	e, ok := ix.Find(id)
	if !ok {
		return nil, fmt.Errorf("no profile for %q in the catalog", id)
	}
	need, err := profile.ParseVersion(e.MinFlashVersion)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", id, err)
	}
	if !dev && have.Less(need) {
		return nil, &TooOldError{ID: id, Required: need, Have: have}
	}
	if !safePath(e.Path) {
		return nil, fmt.Errorf("%s: unsafe profile path %q", id, e.Path)
	}
	data, err := c.fetch(ctx, e.Path)
	if err != nil {
		return nil, err
	}
	if got := profile.Digest(data); got != e.SHA256 {
		return nil, fmt.Errorf("%s: profile digest %s does not match the index %s", id, got, e.SHA256)
	}
	p, err := profile.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", id, err)
	}
	if p.ID != id || p.MinFlashVersion != e.MinFlashVersion {
		return nil, fmt.Errorf("%s: profile disagrees with the index", id)
	}
	return p, nil
}

func safePath(p string) bool {
	return p != "" && !strings.HasPrefix(p, "/") && path.Clean(p) == p && !strings.HasPrefix(p, "..") &&
		strings.HasPrefix(p, profile.DevicesDir+"/")
}

func (c *Catalog) fetch(ctx context.Context, name string) ([]byte, error) {
	if !strings.Contains(c.Source, "://") {
		return readLimited(filepath.Join(c.Source, filepath.FromSlash(name)))
	}
	data, err := c.download(ctx, strings.TrimRight(c.Source, "/")+"/"+name)
	if err == nil {
		c.store(name, data)
		return data, nil
	}
	if cached, cerr := c.cached(name); cerr == nil {
		return cached, nil
	}
	return nil, err
}

func (c *Catalog) download(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxDocument+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxDocument {
		return nil, fmt.Errorf("%s: larger than %d bytes", url, maxDocument)
	}
	return data, nil
}

func (c *Catalog) cachePath(name string) string {
	if c.Cache == "" {
		return ""
	}
	return filepath.Join(c.Cache, filepath.FromSlash(name))
}

func (c *Catalog) store(name string, data []byte) {
	p := c.cachePath(name)
	if p == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return
	}
	tmp := p + ".part"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return
	}
	_ = os.Rename(tmp, p)
}

func (c *Catalog) cached(name string) ([]byte, error) {
	p := c.cachePath(name)
	if p == "" {
		return nil, errors.New("no cache")
	}
	return readLimited(p)
}

func readLimited(p string) ([]byte, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxDocument+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxDocument {
		return nil, fmt.Errorf("%s: larger than %d bytes", p, maxDocument)
	}
	return data, nil
}
