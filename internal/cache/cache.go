package cache

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Item struct {
	ID   int
	URL  string
	Mime string
}

type Cache struct {
	Dir  string
	HTTP *http.Client
	sem  chan struct{}
}

func New(dir string) *Cache {
	return &Cache{
		Dir: dir,
		HTTP: &http.Client{
			Timeout: 10 * time.Minute,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return errors.New("too many redirects")
				}
				req.Header.Del("Authorization")
				return nil
			},
		},
		sem: make(chan struct{}, 2),
	}
}

func Resolve(base, raw string) (string, error) {
	b, err := url.Parse(strings.TrimRight(base, "/"))
	if err != nil {
		return "", err
	}
	ref, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	return b.ResolveReference(ref).String(), nil
}

func (c *Cache) Ready(id int, rawURL string) bool {
	got, err := os.ReadFile(c.sidecar(id, ".url"))
	if err != nil || string(got) != rawURL {
		return false
	}
	info, err := os.Stat(c.final(id))
	return err == nil && info.Mode().IsRegular()
}

func (c *Cache) Open(id int) (*os.File, string, error) {
	f, err := os.Open(c.final(id))
	if err != nil {
		return nil, "", err
	}
	mime, err := os.ReadFile(c.sidecar(id, ".mime"))
	if err != nil {
		f.Close()
		return nil, "", err
	}
	return f, string(mime), nil
}

func (c *Cache) Reconcile(ctx context.Context, base string, items []Item) error {
	if err := os.MkdirAll(c.Dir, 0o700); err != nil {
		return err
	}
	want := map[int]struct{}{}
	var wg sync.WaitGroup
	errCh := make(chan error, len(items))
	for _, item := range items {
		want[item.ID] = struct{}{}
		wg.Add(1)
		go func(item Item) {
			defer wg.Done()
			if err := c.ensure(ctx, base, item); err != nil {
				errCh <- err
			}
		}(item)
	}
	wg.Wait()
	close(errCh)
	var errs []error
	for err := range errCh {
		errs = append(errs, err)
	}
	if err := c.deleteMissing(want); err != nil {
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

func (c *Cache) ensure(ctx context.Context, base string, item Item) error {
	if c.Ready(item.ID, item.URL) {
		return nil
	}
	resolved, err := Resolve(base, item.URL)
	if err != nil {
		return err
	}
	return c.download(ctx, item, resolved)
}

func (c *Cache) download(ctx context.Context, item Item, resolved string) error {
	select {
	case c.sem <- struct{}{}:
		defer func() { <-c.sem }()
	case <-ctx.Done():
		return ctx.Err()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, resolved, nil)
	if err != nil {
		return err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %d: status %d", item.ID, resp.StatusCode)
	}
	partial := c.sidecar(item.ID, ".partial")
	f, err := os.OpenFile(partial, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, resp.Body); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(partial, c.final(item.ID)); err != nil {
		return err
	}
	if err := os.WriteFile(c.sidecar(item.ID, ".url"), []byte(item.URL), 0o600); err != nil {
		return err
	}
	return os.WriteFile(c.sidecar(item.ID, ".mime"), []byte(item.Mime), 0o600)
}

func (c *Cache) deleteMissing(want map[int]struct{}) error {
	entries, err := os.ReadDir(c.Dir)
	if err != nil {
		return err
	}
	seen := map[int]struct{}{}
	for _, entry := range entries {
		name := entry.Name()
		idPart := name
		if dot := strings.IndexByte(name, '.'); dot >= 0 {
			idPart = name[:dot]
		}
		id, err := strconv.Atoi(idPart)
		if err != nil {
			continue
		}
		if _, ok := want[id]; ok {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		for _, suffix := range []string{"", ".url", ".mime", ".partial"} {
			_ = os.Remove(filepath.Join(c.Dir, strconv.Itoa(id)+suffix))
		}
	}
	return nil
}

func (c *Cache) final(id int) string {
	return filepath.Join(c.Dir, strconv.Itoa(id))
}

func (c *Cache) sidecar(id int, suffix string) string {
	return filepath.Join(c.Dir, strconv.Itoa(id)+suffix)
}
