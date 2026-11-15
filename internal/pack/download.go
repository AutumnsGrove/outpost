package pack

import (
	"context"
	"crypto/md5"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

// download fetches every target, a few at a time, and reports what it moved.
func download(ctx context.Context, opts Options, targets []target, log *slog.Logger) (int, int64, error) {
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
		count    int
		bytes    atomic.Int64
		sem      = make(chan struct{}, workers)
	)

	fail := func(err error) {
		mu.Lock()
		if firstErr == nil {
			firstErr = err
		}
		mu.Unlock()
	}

	for _, t := range targets {
		wg.Add(1)
		go func(t target) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			mu.Lock()
			stop := firstErr != nil
			mu.Unlock()
			if stop || ctx.Err() != nil {
				return
			}

			n, unchanged, err := fetchFile(ctx, opts.Dir, t, opts.Force)
			if err != nil {
				fail(err)
				return
			}
			bytes.Add(n)
			if !unchanged {
				log.Debug("fetched", "file", t.rel, "bytes", n)
			}
			mu.Lock()
			count++
			mu.Unlock()
		}(t)
	}
	wg.Wait()

	return count, bytes.Load(), firstErr
}

// fetchFile downloads one file and verifies it against the pack's checksum. A
// file that already matches is left alone, which is what makes a second fetch of
// the same pack cheap.
func fetchFile(ctx context.Context, dir string, t target, force bool) (n int64, unchanged bool, err error) {
	dest := filepath.Join(dir, filepath.FromSlash(t.rel))

	if !force && t.sum != "" {
		ok, err := matches(dest, t.algo, t.sum)
		if err != nil {
			return 0, false, err
		}
		if ok {
			return 0, true, nil
		}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, t.url, nil)
	if err != nil {
		return 0, false, err
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := httpClient.Do(req)
	if err != nil {
		return 0, false, fmt.Errorf("download %s: %w", t.rel, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return 0, false, fmt.Errorf("download %s: %s: %.160s", t.rel, resp.Status, strings.TrimSpace(string(body)))
	}

	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return 0, false, err
	}
	// Written beside the target and renamed: a half-downloaded jar in mods/ is a
	// server that fails to start for reasons nothing in the log explains.
	tmp := dest + ".part"
	out, err := os.Create(tmp)
	if err != nil {
		return 0, false, err
	}

	var h hash.Hash
	if t.algo != "" {
		h = hasher(t.algo)
	}
	var body io.Writer = out
	if h != nil {
		body = io.MultiWriter(out, h)
	}

	n, err = io.Copy(body, resp.Body)
	if err != nil {
		out.Close()
		os.Remove(tmp)
		return 0, false, fmt.Errorf("download %s: %w", t.rel, err)
	}
	if err := out.Close(); err != nil {
		os.Remove(tmp)
		return 0, false, err
	}

	if h != nil {
		got := hex.EncodeToString(h.Sum(nil))
		if !strings.EqualFold(got, t.sum) {
			os.Remove(tmp)
			return 0, false, fmt.Errorf("%s: downloaded file does not match the pack's %s (%s)", t.rel, t.algo, t.url)
		}
	}

	if err := os.Rename(tmp, dest); err != nil {
		os.Remove(tmp)
		return 0, false, err
	}
	return n, false, nil
}

func hasher(algo string) hash.Hash {
	switch algo {
	case "sha512":
		return sha512.New()
	case "sha1":
		return sha1.New()
	case "sha256":
		return sha256.New()
	case "md5":
		return md5.New()
	}
	return nil
}

// matches reports whether an existing file already is the wanted bytes.
func matches(p, algo, want string) (bool, error) {
	if algo == "" {
		return false, nil
	}
	f, err := os.Open(p)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	defer f.Close()

	h := hasher(algo)
	if _, err := io.Copy(h, f); err != nil {
		return false, err
	}
	return strings.EqualFold(hex.EncodeToString(h.Sum(nil)), want), nil
}

// pruneLocal removes files under mods/ and root/ that this fetch did not write.
// Only those two trees: scripts/ and server/ are not the pack's to prune.
func pruneLocal(dir string, written map[string]bool) ([]string, error) {
	var removed []string
	for _, tree := range []string{"mods", "root"} {
		base := filepath.Join(dir, tree)
		err := filepath.WalkDir(base, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				if errors.Is(err, os.ErrNotExist) {
					return nil
				}
				return err
			}
			if d.IsDir() {
				return nil
			}
			rel, err := filepath.Rel(dir, p)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if written[rel] {
				return nil
			}
			if err := os.Remove(p); err != nil {
				return err
			}
			removed = append(removed, rel)
			return nil
		})
		if err != nil {
			return removed, fmt.Errorf("prune %s: %w", tree, err)
		}
	}
	sort.Strings(removed)
	return removed, nil
}
