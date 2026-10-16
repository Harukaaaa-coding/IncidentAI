package projects

import (
	"errors"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

var redactPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)(authorization\s*["']?\s*[:=]\s*["']?(?:bearer\s+|basic\s+)?)[^\s"',;]+`),
	regexp.MustCompile(`(?i)((?:api[_-]?key|access[_-]?token|refresh[_-]?token|auth[_-]?token|token|password|passwd|secret|client[_-]?secret|cookie|set-cookie)\s*["']?\s*[:=]\s*["']?)[^\s"',;]+`),
	regexp.MustCompile(`(?i)([?&](?:key|token|api_key|access_token|secret|password)=)[^&#\s]+`),
}
var privateKeyPattern = regexp.MustCompile(`(?s)-----BEGIN [A-Z ]*PRIVATE KEY-----.*?(?:-----END [A-Z ]*PRIVATE KEY-----|$)`)
var tokenPattern = regexp.MustCompile(`\b(?:sk-[A-Za-z0-9_-]{12,}|gh[pousr]_[A-Za-z0-9_]{12,}|github_pat_[A-Za-z0-9_]{12,}|AKIA[A-Z0-9]{16}|eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+)\b`)
var urlCredentials = regexp.MustCompile(`(?i)([a-z][a-z0-9+.-]*://)[^/@\s]+:[^/@\s]+@`)
var emailPattern = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)

func Redact(s string) string {
	s = privateKeyPattern.ReplaceAllString(s, "[REDACTED PRIVATE KEY]")
	for _, pattern := range redactPatterns {
		s = pattern.ReplaceAllString(s, "${1}[REDACTED]")
	}
	s = tokenPattern.ReplaceAllString(s, "[REDACTED TOKEN]")
	s = urlCredentials.ReplaceAllString(s, "${1}[REDACTED]@")
	return emailPattern.ReplaceAllString(s, "[REDACTED EMAIL]")
}
func samePath(a, b string) bool { return strings.EqualFold(filepath.Clean(a), filepath.Clean(b)) }
func inside(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel)
}
func safeRepository(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("repository_path is required")
	}
	abs, err := filepath.Abs(strings.TrimSpace(path))
	if err != nil {
		return "", err
	}
	abs = filepath.Clean(abs)
	if strings.HasPrefix(abs, `\\`) || strings.HasPrefix(abs, "//") {
		return "", errors.New("network paths are not permitted")
	}
	info, err := os.Lstat(abs)
	if err != nil {
		return "", errors.New("repository directory is not accessible")
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("repository must be a real directory")
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil || !samePath(resolved, abs) {
		return "", errors.New("symlinked repository paths are not permitted")
	}
	if filepath.Dir(abs) == abs {
		return "", errors.New("filesystem roots cannot be projects")
	}
	for _, part := range strings.Split(strings.ToLower(filepath.ToSlash(abs)), "/") {
		switch part {
		case ".ssh", ".aws", ".azure", ".kube", ".gnupg", ".codex":
			return "", errors.New("credential and application-data directories cannot be projects")
		case "appdata":
			if !inside(os.TempDir(), abs) || samePath(os.TempDir(), abs) {
				return "", errors.New("application-data directories cannot be projects")
			}
		}
	}
	if samePath(os.TempDir(), abs) {
		return "", errors.New("the shared temporary directory cannot be a project")
	}
	if home, err := os.UserHomeDir(); err == nil && inside(abs, home) {
		return "", errors.New("home directories and their ancestors cannot be projects")
	}
	for _, reserved := range []string{os.Getenv("SystemRoot"), os.Getenv("ProgramFiles"), os.Getenv("ProgramFiles(x86)"), os.Getenv("ProgramData"), "/etc", "/proc", "/sys", "/dev", "/bin", "/usr"} {
		if reserved != "" && (inside(reserved, abs) || inside(abs, reserved)) {
			return "", errors.New("system directories cannot be projects")
		}
	}
	return abs, nil
}
func secretPath(path string) bool {
	for _, part := range strings.Split(strings.ToLower(filepath.ToSlash(path)), "/") {
		if part == ".git" || part == ".ssh" || part == ".aws" || part == ".azure" || part == ".kube" || part == ".env" || strings.HasPrefix(part, ".env.") || strings.Contains(part, "credential") || strings.Contains(part, "secret") || strings.Contains(part, "privatekey") || strings.HasPrefix(part, "id_rsa") || strings.HasPrefix(part, "id_ed25519") {
			return true
		}
	}
	base := strings.ToLower(filepath.Base(path))
	ext := strings.ToLower(filepath.Ext(path))
	return strings.HasPrefix(base, "config.") || strings.HasPrefix(base, "settings.") || ext == ".pem" || ext == ".key" || ext == ".p12" || ext == ".pfx" || ext == ".keystore" || ext == ".env"
}
func safeLogPath(repo, path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", nil
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(repo, path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if !inside(repo, abs) {
		return "", errors.New("log_path must be inside the selected repository")
	}
	rel, _ := filepath.Rel(repo, abs)
	if secretPath(rel) {
		return "", errors.New("secret or configuration paths cannot be read as logs")
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil || !samePath(abs, resolved) {
		return "", errors.New("log path must exist and may not contain symlinks")
	}
	info, err := os.Stat(abs)
	if err != nil || (!info.IsDir() && !info.Mode().IsRegular()) {
		return "", errors.New("log path must be a file or directory")
	}
	if !info.IsDir() && !logFile(abs) {
		return "", errors.New("log file must have .log, .txt, .jsonl or .ndjson extension")
	}
	return abs, nil
}
func logFile(path string) bool {
	if secretPath(path) {
		return false
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".log", ".txt", ".jsonl", ".ndjson":
		return true
	}
	return false
}
func healthTarget(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, errors.New("health_url must be an http(s) loopback URL")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("health_url must not include credentials, query strings or fragments")
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	if !strings.EqualFold(host, "localhost") && (ip == nil || !ip.IsLoopback()) {
		return nil, errors.New("health_url is limited to localhost or loopback IP addresses")
	}
	return u, nil
}
