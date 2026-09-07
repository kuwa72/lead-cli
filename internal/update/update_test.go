package update

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/kuwa72/lead-cli/internal/testutil"
)

var errNoBrew = errors.New("no brew")

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		current, latest string
		want            int // -1 behind, 0 same, 1 ahead
	}{
		{"v0.1.0", "v0.2.0", -1},
		{"v0.2.0", "v0.2.0", 0},
		{"v1.10.0", "v1.2.0", 1}, // numeric, not lexical
		{"0.1.0", "v0.1.1", -1},  // missing v tolerated
		{"dev", "v0.1.0", -1},    // non-release is always behind
		{"v0.1.0", "dev", 1},
	}
	for _, c := range cases {
		if got := CompareVersions(c.current, c.latest); got != c.want {
			t.Errorf("CompareVersions(%q, %q) = %d, want %d", c.current, c.latest, got, c.want)
		}
	}
}

func TestCheckReportsAvailability(t *testing.T) {
	fake := &testutil.FakeGhClient{LatestTag: "v0.2.0"}

	info, err := Check(context.Background(), fake, "o/r", "v0.1.0")
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	if !info.Available || info.Latest != "v0.2.0" {
		t.Errorf("Info = %+v, want available v0.2.0", info)
	}

	info, err = Check(context.Background(), fake, "o/r", "v0.2.0")
	if err != nil {
		t.Fatalf("Check up-to-date: %v", err)
	}
	if info.Available {
		t.Errorf("Info = %+v, want no update when equal", info)
	}
}

func TestBrewManagedDetection(t *testing.T) {
	lookBrew := func(string) (string, error) { return "/opt/homebrew/bin/brew", nil }
	prefix := func() (string, error) { return "/opt/homebrew", nil }

	if !BrewManaged("/opt/homebrew/Cellar/lead/0.1.0/bin/lead", lookBrew, prefix) {
		t.Error("cellar path not detected as brew-managed")
	}
	if BrewManaged("/home/u/.local/bin/lead", lookBrew, prefix) {
		t.Error("local path detected as brew-managed")
	}
	noBrew := func(string) (string, error) { return "", errNoBrew }
	if BrewManaged("/opt/homebrew/Cellar/lead/0.1.0/bin/lead", noBrew, prefix) {
		t.Error("brew-managed without brew on PATH")
	}
}

func serveRelease(t *testing.T, payload []byte, tamperSums bool) *httptest.Server {
	t.Helper()
	sum := fmt.Sprintf("%x", sha256.Sum256(payload))
	if tamperSums {
		sum = strings.Repeat("0", 64)
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "checksums.txt"):
			fmt.Fprintf(w, "%s  %s\n", sum, "lead_linux_amd64.tar.gz")
		case strings.HasSuffix(r.URL.Path, ".tar.gz"):
			w.Write(payload)
		default:
			http.NotFound(w, r)
		}
	}))
}

func writeExe(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "lead")
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestReplaceDownloadsVerifiesAndSwaps(t *testing.T) {
	payload := []byte("new-binary-bytes")
	srv := serveRelease(t, payload, false)
	defer srv.Close()

	exe := writeExe(t, "old-binary")
	if err := Replace(context.Background(), Options{
		BaseURL: srv.URL, Version: "v0.2.0", ExePath: exe, GOOS: "linux", GOARCH: "amd64",
	}); err != nil {
		t.Fatalf("Replace: %v", err)
	}
	got, err := os.ReadFile(exe)
	if err != nil || string(got) != string(payload) {
		t.Errorf("exe content = %q, %v; want replaced payload", got, err)
	}
	if st, err := os.Stat(exe); err != nil || st.Mode().Perm()&0o111 == 0 {
		t.Errorf("exe not executable after replace: %v %v", st, err)
	}
}

func TestReplaceRefusesTamperedPayload(t *testing.T) {
	payload := []byte("evil-binary")
	srv := serveRelease(t, payload, true) // checksums mismatch
	defer srv.Close()

	exe := writeExe(t, "old-binary")
	if err := Replace(context.Background(), Options{
		BaseURL: srv.URL, Version: "v0.2.0", ExePath: exe, GOOS: "linux", GOARCH: "amd64",
	}); err == nil {
		t.Fatal("Replace with bad checksum = nil, want refusal")
	} else if !strings.Contains(err.Error(), "checksum") {
		t.Errorf("error = %q, want checksum mismatch", err)
	}
	if got, _ := os.ReadFile(exe); string(got) != "old-binary" {
		t.Errorf("exe touched despite mismatch: %q", got)
	}
}

func TestReplaceFailsCleanlyOnHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusNotFound)
	}))
	defer srv.Close()

	exe := writeExe(t, "old-binary")
	if err := Replace(context.Background(), Options{
		BaseURL: srv.URL, Version: "v9.9.9", ExePath: exe, GOOS: "linux", GOARCH: "amd64",
	}); err == nil {
		t.Error("Replace on 404 = nil, want error")
	}
	if got, _ := os.ReadFile(exe); string(got) != "old-binary" {
		t.Errorf("exe touched on failed download: %q", got)
	}
}
