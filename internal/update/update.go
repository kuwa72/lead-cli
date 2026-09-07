// Package update implements `lead update`: version check and self-replacement
// for direct-download installs (issue #44). See docs/rfc-26-distribution.md §7.
//
// Rules: no background updates; one canonical path per install route; never
// touch brew-managed binaries (print `brew upgrade` guidance instead).
// Asset layout contract (see #42 goreleaser): releases host
// `lead_<GOOS>_<GOARCH>.tar.gz` plus a `checksums.txt` index.
package update

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/kuwa72/lead-cli/internal/ports"
)

// UpstreamRepo is the release source for update checks.
const UpstreamRepo = "kuwa72/lead-cli"

// Info is the result of a version check.
type Info struct {
	Current   string
	Latest    string
	Available bool
}

// CompareVersions compares release versions numerically (-1/0/1).
// A leading "v" is optional; unparseable versions compare as strings,
// with a non-release (e.g. "dev") always considered behind a release.
func CompareVersions(current, latest string) int {
	c, cOK := parseVer(current)
	l, lOK := parseVer(latest)
	if !cOK || !lOK {
		switch {
		case current == latest:
			return 0
		case !cOK && lOK:
			return -1
		case cOK && !lOK:
			return 1
		default:
			return strings.Compare(current, latest)
		}
	}
	for i := 0; i < 3; i++ {
		if c[i] != l[i] {
			if c[i] < l[i] {
				return -1
			}
			return 1
		}
	}
	return 0
}

func parseVer(s string) ([3]int, bool) {
	var v [3]int
	s = strings.TrimPrefix(strings.TrimSpace(s), "v")
	parts := strings.Split(s, ".")
	if len(parts) != 3 {
		return v, false
	}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return v, false
		}
		v[i] = n
	}
	return v, true
}

// Check resolves the latest release tag and compares it to current.
func Check(ctx context.Context, gh ports.GhClient, repo, current string) (Info, error) {
	latest, err := gh.LatestReleaseTag(ctx, repo)
	if err != nil {
		return Info{}, fmt.Errorf("latest release: %w", err)
	}
	latest = strings.TrimSpace(latest)
	return Info{Current: current, Latest: latest, Available: CompareVersions(current, latest) < 0}, nil
}

// BrewManaged reports whether exePath sits under the Homebrew prefix.
// lookPath finds brew; brewPrefix runs `brew --prefix` (both injectable).
func BrewManaged(exePath string, lookPath func(string) (string, error), brewPrefix func() (string, error)) bool {
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	if _, err := lookPath("brew"); err != nil {
		return false
	}
	prefix := ""
	if brewPrefix != nil {
		prefix, _ = brewPrefix()
	} else {
		out, err := exec.Command("brew", "--prefix").Output()
		if err != nil {
			return false
		}
		prefix = strings.TrimSpace(string(out))
	}
	if prefix == "" {
		return false
	}
	abs, err := filepath.Abs(exePath)
	if err != nil {
		return false
	}
	return abs == prefix || strings.HasPrefix(abs, prefix+string(os.PathSeparator))
}

// AssetName is the release asset contract shared with #42 (goreleaser).
func AssetName(goos, goarch, version string) string {
	_ = version
	return fmt.Sprintf("lead_%s_%s.tar.gz", goos, goarch)
}

// Options controls one self-replacement.
type Options struct {
	BaseURL string // release download base; "" = github.com/kuwa72/lead-cli/releases/download
	Version string // tag to install (e.g. v0.2.0)
	ExePath string // binary to replace
	GOOS    string // override runtime.GOOS (tests)
	GOARCH  string // override runtime.GOARCH (tests)
}

func (o *Options) baseURL() string {
	if o.BaseURL != "" {
		return strings.TrimSuffix(o.BaseURL, "/")
	}
	return "https://github.com/kuwa72/lead-cli/releases/download"
}

func (o *Options) goos() string {
	if o.GOOS != "" {
		return o.GOOS
	}
	return runtime.GOOS
}

func (o *Options) goarch() string {
	if o.GOARCH != "" {
		return o.GOARCH
	}
	return runtime.GOARCH
}

// Replace downloads the release asset, verifies its sha256 against
// checksums.txt, and atomically swaps the running binary. On any failure
// the existing binary is untouched.
func Replace(ctx context.Context, opts Options) error {
	asset := AssetName(opts.goos(), opts.goarch(), opts.Version)
	base := fmt.Sprintf("%s/%s", opts.baseURL(), opts.Version)

	sums, err := fetch(ctx, base+"/checksums.txt")
	if err != nil {
		return fmt.Errorf("download checksums: %w", err)
	}
	want, err := findChecksum(string(sums), asset)
	if err != nil {
		return err
	}
	body, err := fetch(ctx, base+"/"+asset)
	if err != nil {
		return fmt.Errorf("download %s: %w", asset, err)
	}
	if got := fmt.Sprintf("%x", sha256.Sum256(body)); got != want {
		return fmt.Errorf("checksum mismatch for %s: want %s, got %s", asset, want, got)
	}

	dir := filepath.Dir(opts.ExePath)
	tmp, err := os.CreateTemp(dir, "lead-update-*")
	if err != nil {
		return fmt.Errorf("temp file: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(body); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("write temp: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("close temp: %w", err)
	}
	if err := os.Chmod(tmpName, 0o755); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("chmod temp: %w", err)
	}
	if err := os.Rename(tmpName, opts.ExePath); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("replace binary: %w", err)
	}
	return nil
}

func fetch(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %s", url, resp.Status)
	}
	return io.ReadAll(resp.Body)
}

// findChecksum parses goreleaser-style checksums.txt ("<sha>  <file>").
func findChecksum(sums, asset string) (string, error) {
	for _, line := range strings.Split(sums, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == asset {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("checksums.txt: no entry for %s", asset)
}
