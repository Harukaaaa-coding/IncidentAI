package projects

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func TestAuditRedactsCredentialsInStructuredAndPlainEvidence(t *testing.T) {
	for _, tc := range []struct{ name, input, secret string }{
		{"JSON authorization", `{"Authorization":"Bearer arbitrary-sensitive-value"}`, "arbitrary-sensitive-value"},
		{"plain authorization", "Authorization: Basic synthetic-credential-value", "synthetic-credential-value"},
		{"database URI", "postgres://app:database-password@db:5432/app", "database-password"},
		{"Redis URI", "redis://app:redis-password@localhost:6379", "redis-password"},
		{"plain token", "token=synthetic-token-value", "synthetic-token-value"},
		{"JSON key", `{"api_key":"synthetic-key-value"}`, "synthetic-key-value"},
		{"private key", "-----BEGIN PRIVATE KEY-----\nsynthetic-key-material\n-----END PRIVATE KEY-----", "synthetic-key-material"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := Redact(tc.input)
			if strings.Contains(got, tc.secret) || !strings.Contains(got, "REDACTED") {
				t.Fatalf("synthetic credential not redacted: %q", got)
			}
		})
	}
}

func TestAuditHealthOnlyLoopbackWithoutCredentials(t *testing.T) {
	for _, raw := range []string{"https://example.com/health", "http://169.254.169.254/latest/meta-data", "http://192.168.1.10/health", "file:///etc/passwd", "http://localhost.attacker.example/health", "http://127.0.0.1@attacker.example/health", "http://user:pass@localhost/health", "http://localhost/health?token=value", "http://localhost/health#fragment", "http://2130706433/health"} {
		t.Run(raw, func(t *testing.T) {
			if _, err := healthTarget(raw); err == nil {
				t.Fatal("unsafe health target accepted")
			}
		})
	}
	for _, raw := range []string{"http://localhost:8080/health", "http://127.0.0.1:8080/health", "http://[::1]:8080/health"} {
		if _, err := healthTarget(raw); err != nil {
			t.Fatalf("valid loopback target rejected: %v", err)
		}
	}
}

func TestAuditHealthDoesNotFollowRedirectOrUseProxy(t *testing.T) {
	var destinationCalls, proxyCalls atomic.Int32
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { destinationCalls.Add(1) }))
	defer destination.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { proxyCalls.Add(1) }))
	defer proxy.Close()
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("HTTPS_PROXY", proxy.URL)
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, destination.URL, http.StatusFound) }))
	defer redirect.Close()
	check, _ := healthCheck(context.Background(), redirect.URL)
	if check.Status != "failed" || destinationCalls.Load() != 0 || proxyCalls.Load() != 0 {
		t.Fatalf("redirect/proxy escaped health boundary: check=%+v target=%d proxy=%d", check, destinationCalls.Load(), proxyCalls.Load())
	}
}

func TestAuditSafeReadsDoNotFollowSymlinks(t *testing.T) {
	repo := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "outside.go"), []byte("synthetic-outside-content"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "outside.go"), filepath.Join(repo, "escape.go")); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	root, err := os.OpenRoot(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	if _, err := readBounded(root, "escape.go", 1024); err == nil {
		t.Fatal("symlink escape was readable")
	}
	inv, err := scanSources(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(inv.files) != 0 {
		t.Fatalf("symlink included as source: %+v", inv.files)
	}
	if _, err := readBounded(root, "../outside.go", 1024); err == nil {
		t.Fatal("parent traversal accepted")
	}
}

func auditGit(t *testing.T, repo, input string, args ...string) string {
	t.Helper()
	base := []string{"-c", "core.hooksPath=" + os.DevNull, "-c", "core.fsmonitor=false", "-c", "commit.gpgSign=false", "-C", repo}
	cmd := exec.Command("git", append(base, args...)...)
	cmd.Stdin = strings.NewReader(input)
	env := []string{}
	for _, item := range os.Environ() {
		if !strings.HasPrefix(strings.ToUpper(item), "GIT_") {
			env = append(env, item)
		}
	}
	cmd.Env = append(env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_AUTHOR_NAME=Audit Fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=Audit Fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("fixture git %v failed: %s %v", args, out, err)
	}
	return strings.TrimSpace(string(out))
}

func TestAuditGitComparisonNeverExecutesRepositoryFilters(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	repo := t.TempDir()
	template := t.TempDir()
	auditGit(t, repo, "", "init", "--template="+template)
	if err := os.WriteFile(filepath.Join(repo, "service.go"), []byte("package fixture\nconst RetryLimit = 5\n"), 0600); err != nil {
		t.Fatal(err)
	}
	auditGit(t, repo, "", "add", "--", "service.go")
	tree := auditGit(t, repo, "", "write-tree")
	commit := auditGit(t, repo, "fixture baseline\n", "commit-tree", tree)
	auditGit(t, repo, "", "update-ref", "HEAD", commit)
	auditGit(t, repo, "", "config", "filter.auditclean.clean", "echo executed > filter-ran.txt; cat")
	auditGit(t, repo, "", "config", "core.fsmonitor", "echo executed > fsmonitor-ran.txt")
	auditGit(t, repo, "", "config", "diff.external", "echo executed > diff-ran.txt")
	if err := os.WriteFile(filepath.Join(repo, ".gitattributes"), []byte("*.go filter=auditclean\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "service.go"), []byte("package fixture\nconst RetryLimit = 2\n"), 0600); err != nil {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	inv, err := scanSources(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	status, diff, err := gitEvidence(context.Background(), Project{RepositoryPath: repo}, inv)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(status, "service.go") || !strings.Contains(diff, "RetryLimit = 5") || !strings.Contains(diff, "RetryLimit = 2") {
		t.Fatalf("raw comparison unavailable: status=%s diff=%s", status, diff)
	}
	for _, marker := range []string{"filter-ran.txt", "fsmonitor-ran.txt", "diff-ran.txt"} {
		if _, err := os.Stat(filepath.Join(repo, marker)); !os.IsNotExist(err) {
			t.Fatalf("repository-controlled command ran: %s (%v)", marker, err)
		}
	}
}

func TestAuditRGIgnoresExternalConfig(t *testing.T) {
	if _, err := exec.LookPath("rg"); err != nil {
		t.Skip("ripgrep unavailable")
	}
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "service.go"), []byte("package fixture\n// SyntheticPaymentError is handled here.\n"), 0600); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(t.TempDir(), "rg.config")
	if err := os.WriteFile(config, []byte("--invalid-option-for-audit\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RIPGREP_CONFIG_PATH", config)
	root, err := os.OpenRoot(repo)
	if err != nil {
		t.Fatal(err)
	}
	defer root.Close()
	inv, err := scanSources(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	matches, limitations := sourceMatches(context.Background(), Project{RepositoryPath: repo}, inv, []string{"SyntheticPaymentError"})
	if len(matches) != 1 || matches[0].Path != "service.go" || matches[0].Line != 2 {
		t.Fatalf("external rg config interfered with safe stdin search: matches=%+v limitations=%v", matches, limitations)
	}
}
