package projects

import (
	"bytes"
	"context"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const maxSourceFiles = 400
const maxSourceBytes = 128 * 1024
const maxLogBytes = 256 * 1024

type sourceFile struct {
	path   string
	digest string
	size   int64
}
type inventory struct {
	files   []sourceFile
	skipped int
	limited bool
}

func sourceAllowed(path string) bool {
	if secretPath(path) || strings.ContainsAny(path, "\r\n\x00") {
		return false
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".go", ".py", ".js", ".jsx", ".ts", ".tsx", ".java", ".rs", ".rb", ".c", ".cpp", ".h", ".cs", ".php", ".vue", ".svelte", ".html", ".css", ".sql", ".md", ".mod", ".sum":
		return true
	}
	return false
}
func ignoredDirectory(name string) bool {
	switch strings.ToLower(name) {
	case ".git", ".idea", ".vscode", ".codex", ".agents", "node_modules", "vendor", "dist", "build", "target", ".venv", "venv", "__pycache__", "data", "coverage":
		return true
	}
	return secretPath(name)
}
func scanSources(ctx context.Context, root *os.Root) (inventory, error) {
	inv := inventory{files: []sourceFile{}}
	visited := 0
	var walk func(string, int) error
	walk = func(dir string, depth int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if depth > 12 || visited > 5000 || len(inv.files) >= maxSourceFiles {
			inv.limited = true
			return nil
		}
		f, err := root.Open(dir)
		if err != nil {
			return err
		}
		remaining := 5001 - visited
		entries, err := f.ReadDir(remaining)
		f.Close()
		if err != nil && err != io.EOF {
			return err
		}
		if len(entries) >= remaining {
			inv.limited = true
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, entry := range entries {
			visited++
			if len(inv.files) >= maxSourceFiles || visited > 5000 {
				inv.limited = true
				break
			}
			if entry.Type()&os.ModeSymlink != 0 {
				inv.skipped++
				continue
			}
			path := filepath.Join(dir, entry.Name())
			if entry.IsDir() {
				if ignoredDirectory(entry.Name()) {
					continue
				}
				if err := walk(path, depth+1); err != nil {
					return err
				}
				continue
			}
			if !sourceAllowed(path) {
				continue
			}
			info, err := entry.Info()
			if err != nil || !info.Mode().IsRegular() {
				continue
			}
			if info.Size() > maxSourceBytes {
				inv.skipped++
				continue
			}
			content, err := readBounded(root, path, maxSourceBytes)
			if err != nil {
				inv.skipped++
				continue
			}
			if bytes.IndexByte(content, 0) >= 0 {
				inv.skipped++
				continue
			}
			sum := sha256.Sum256(content)
			inv.files = append(inv.files, sourceFile{path: path, digest: hex.EncodeToString(sum[:]), size: int64(len(content))})
		}
		return nil
	}
	err := walk(".", 0)
	return inv, err
}
func readBounded(root *os.Root, path string, limit int) ([]byte, error) {
	if err := noSymlink(root, path); err != nil {
		return nil, err
	}
	f, err := root.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, errors.New("not a regular file")
	}
	b, err := io.ReadAll(io.LimitReader(f, int64(limit)+1))
	if len(b) > limit {
		return nil, errors.New("file exceeds read limit")
	}
	return b, err
}
func noSymlink(root *os.Root, path string) error {
	current := ""
	for _, part := range strings.Split(filepath.Clean(path), string(filepath.Separator)) {
		if part == "." {
			continue
		}
		if part == ".." {
			return errors.New("path traversal blocked")
		}
		current = filepath.Join(current, part)
		info, err := root.Lstat(current)
		if err != nil {
			return err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return errors.New("symlink traversal blocked")
		}
	}
	return nil
}
func sameInventory(a, b inventory) bool {
	if len(a.files) != len(b.files) {
		return false
	}
	for i := range a.files {
		if a.files[i] != b.files[i] {
			return false
		}
	}
	return true
}

func healthCheck(ctx context.Context, raw string) (Check, string) {
	if raw == "" {
		return Check{"health", "skipped", "未配置本机健康检查地址"}, ""
	}
	u, err := healthTarget(raw)
	if err != nil {
		return Check{"health", "failed", err.Error()}, ""
	}
	dialer := &net.Dialer{Timeout: 3 * time.Second}
	transport := &http.Transport{Proxy: nil, DisableKeepAlives: true, DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		if strings.EqualFold(host, "localhost") {
			host = "127.0.0.1"
		}
		ip := net.ParseIP(host)
		if ip == nil || !ip.IsLoopback() {
			return nil, errors.New("non-loopback destination blocked")
		}
		return dialer.DialContext(ctx, network, net.JoinHostPort(host, port))
	}}
	defer transport.CloseIdleConnections()
	client := &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return Check{"health", "failed", "无法创建检查请求"}, ""
	}
	req.Header.Set("User-Agent", "IncidentAI-readonly-check/1.0")
	resp, err := client.Do(req)
	if err != nil {
		return Check{"health", "failed", "本机健康检查连接失败或超时（未访问外部网络）"}, Redact(err.Error())
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	status := "passed"
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		status = "failed"
	}
	summary := fmt.Sprintf("GET %s → HTTP %d；仅证明该端点当前响应状态", u.String(), resp.StatusCode)
	return Check{"health", status, summary}, Redact(string(b))
}

func (m *Manager) Validate(ctx context.Context, id string) (Validation, error) {
	p, err := m.Get(id)
	if err != nil {
		return Validation{}, err
	}
	result := Validation{Valid: true, Checks: []Check{}}
	repo, err := safeRepository(p.RepositoryPath)
	if err != nil {
		result.Valid = false
		result.Checks = append(result.Checks, Check{"repository", "failed", err.Error()})
		return result, nil
	}
	root, err := os.OpenRoot(repo)
	if err != nil {
		return Validation{}, err
	}
	defer root.Close()
	inv, err := scanSources(ctx, root)
	if err != nil {
		result.Valid = false
		result.Checks = append(result.Checks, Check{"repository", "failed", "无法完成受限源码枚举"})
	} else {
		result.Checks = append(result.Checks, Check{"repository", "passed", fmt.Sprintf("可读源码 %d 个；不读取配置、密钥和依赖目录", len(inv.files))})
	}
	if p.LogPath == "" {
		result.Checks = append(result.Checks, Check{"logs", "skipped", "未配置日志；调查将缺少运行时故障证据"})
	} else if _, err = safeLogPath(repo, p.LogPath); err != nil {
		result.Valid = false
		result.Checks = append(result.Checks, Check{"logs", "failed", err.Error()})
	} else {
		files, e := logFiles(root, p)
		if e != nil || len(files) == 0 {
			result.Valid = false
			result.Checks = append(result.Checks, Check{"logs", "failed", "未找到可读的允许类型日志文件"})
		} else {
			result.Checks = append(result.Checks, Check{"logs", "passed", fmt.Sprintf("可访问 %d 个日志文件（单次最多读取最新 5 个）", len(files))})
		}
	}
	health, _ := healthCheck(ctx, p.HealthURL)
	result.Checks = append(result.Checks, health)
	if health.Status == "failed" {
		result.Valid = false
	}
	result.Checks = append(result.Checks, Check{"safety", "passed", "只读：不执行项目代码、测试、构建、脚本或生产操作；不向外部模型发送内容"})
	return result, nil
}

type logEntry struct {
	path     string
	modified time.Time
}

func logFiles(root *os.Root, p Project) ([]string, error) {
	if p.LogPath == "" {
		return []string{}, nil
	}
	rel, err := filepath.Rel(p.RepositoryPath, p.LogPath)
	if err != nil {
		return nil, err
	}
	if err = noSymlink(root, rel); err != nil {
		return nil, err
	}
	info, err := root.Stat(rel)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return []string{rel}, nil
	}
	f, err := root.Open(rel)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	entries, err := f.ReadDir(1000)
	if err != nil && err != io.EOF {
		return nil, err
	}
	candidates := []logEntry{}
	for _, entry := range entries {
		path := filepath.Join(rel, entry.Name())
		if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 || !logFile(path) {
			continue
		}
		info, err := entry.Info()
		if err != nil || !info.Mode().IsRegular() {
			continue
		}
		candidates = append(candidates, logEntry{path, info.ModTime()})
	}
	sort.Slice(candidates, func(i, j int) bool { return candidates[i].modified.After(candidates[j].modified) })
	if len(candidates) > 5 {
		candidates = candidates[:5]
	}
	result := []string{}
	for _, c := range candidates {
		result = append(result, c.path)
	}
	return result, nil
}
func tailLog(root *os.Root, path string) (string, bool, error) {
	if err := noSymlink(root, path); err != nil {
		return "", false, err
	}
	f, err := root.Open(path)
	if err != nil {
		return "", false, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return "", false, errors.New("not a regular log file")
	}
	offset := info.Size() - maxLogBytes
	partial := offset > 0
	if partial {
		if _, err = f.Seek(offset, io.SeekStart); err != nil {
			return "", false, err
		}
	}
	b, err := io.ReadAll(io.LimitReader(f, maxLogBytes))
	if bytes.IndexByte(b, 0) >= 0 {
		return "", partial, errors.New("binary log content rejected")
	}
	if partial {
		if n := bytes.IndexByte(b, '\n'); n >= 0 {
			b = b[n+1:]
		}
	}
	return Redact(string(b)), partial, err
}

type boundedBuffer struct {
	bytes.Buffer
	max       int
	truncated bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	remaining := b.max - b.Len()
	if n > remaining {
		b.truncated = true
		p = p[:max(remaining, 0)]
	}
	b.Buffer.Write(p)
	return n, nil
}
func readCommand(ctx context.Context, dir, name string, args ...string) (string, error) {
	return readCommandInput(ctx, dir, name, "", args...)
}
func readCommandInput(ctx context.Context, dir, name, input string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	env := []string{}
	for _, item := range os.Environ() {
		key := strings.ToUpper(strings.SplitN(item, "=", 2)[0])
		if !strings.HasPrefix(key, "GIT_") && key != "RIPGREP_CONFIG_PATH" {
			env = append(env, item)
		}
	}
	cmd.Env = append(env, "GIT_TERMINAL_PROMPT=0", "GIT_OPTIONAL_LOCKS=0", "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_NO_REPLACE_OBJECTS=1", "GIT_NO_LAZY_FETCH=1")
	if input != "" {
		cmd.Stdin = strings.NewReader(input)
	}
	var out boundedBuffer
	out.max = 96 * 1024
	var errs boundedBuffer
	errs.max = 1024
	cmd.Stdout = &out
	cmd.Stderr = &errs
	err := cmd.Run()
	if out.truncated {
		return Redact(out.String()), errors.New("command output truncated at limit")
	}
	if err != nil {
		return Redact(out.String()), err
	}
	return Redact(out.String()), nil
}
func gitArgs(repo string, args ...string) []string {
	base := []string{"--no-pager", "-c", "core.fsmonitor=false", "-c", "core.hooksPath=" + os.DevNull, "-c", "diff.external=", "-C", repo}
	return append(base, args...)
}
func gitEvidence(ctx context.Context, p Project, inv inventory) (string, string, error) {
	root, err := os.OpenRoot(p.RepositoryPath)
	if err != nil {
		return "", "", err
	}
	defer root.Close()
	info, err := root.Lstat(".git")
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", "", errors.New("only standalone Git directories are supported")
	}
	for _, path := range []string{".git/commondir", ".git/objects/info/alternates", ".git/objects/info/http-alternates"} {
		if _, err := root.Lstat(filepath.FromSlash(path)); !os.IsNotExist(err) {
			return "", "", errors.New("external Git object stores are not supported")
		}
	}
	for _, path := range []string{".git/objects", ".git/objects/pack", ".git/refs"} {
		if err := noSymlink(root, filepath.FromSlash(path)); err != nil && !os.IsNotExist(err) {
			return "", "", err
		}
	}
	pack, err := root.Open(filepath.FromSlash(".git/objects/pack"))
	if err == nil {
		entries, _ := pack.ReadDir(1000)
		pack.Close()
		for _, entry := range entries {
			if strings.HasSuffix(entry.Name(), ".promisor") {
				return "", "", errors.New("partial clone is not supported; no objects will be fetched")
			}
		}
	}
	top, err := readCommand(ctx, p.RepositoryPath, "git", gitArgs(p.RepositoryPath, "rev-parse", "--show-toplevel")...)
	if err != nil || !samePath(strings.TrimSpace(top), p.RepositoryPath) {
		return "", "", errors.New("project is not a standalone Git worktree or git is unavailable")
	}
	commit, err := readCommand(ctx, p.RepositoryPath, "git", gitArgs(p.RepositoryPath, "rev-parse", "--verify", "HEAD")...)
	if err != nil {
		return "", "", err
	}
	commit = strings.TrimSpace(commit)
	if !regexp.MustCompile(`^[a-f0-9]{40,64}$`).MatchString(commit) {
		return "", "", errors.New("invalid Git object ID")
	}
	tree, err := readCommand(ctx, p.RepositoryPath, "git", gitArgs(p.RepositoryPath, "ls-tree", "-r", "-z", commit)...)
	if err != nil {
		return "", "", err
	}
	blobs := map[string]string{}
	for _, entry := range strings.Split(tree, "\x00") {
		parts := strings.SplitN(entry, "\t", 2)
		if len(parts) != 2 {
			continue
		}
		fields := strings.Fields(parts[0])
		if len(fields) == 3 && fields[1] == "blob" && strings.HasPrefix(fields[0], "100") {
			blobs[filepath.FromSlash(parts[1])] = fields[2]
		}
	}
	status := []string{}
	excerpts := []string{}
	for _, file := range inv.files {
		if ctx.Err() != nil {
			return "", "", ctx.Err()
		}
		content, err := readBounded(root, file.path, maxSourceBytes)
		if err != nil {
			return "", "", err
		}
		oldID, tracked := blobs[file.path]
		if !tracked {
			status = append(status, "untracked source: "+filepath.ToSlash(file.path))
			continue
		}
		var digest hash.Hash = sha1.New()
		if len(oldID) == 64 {
			digest = sha256.New()
		}
		fmt.Fprintf(digest, "blob %d\x00", len(content))
		digest.Write(content)
		if hex.EncodeToString(digest.Sum(nil)) == oldID {
			continue
		}
		status = append(status, "changed raw source: "+filepath.ToSlash(file.path))
		if len(excerpts) >= 8 {
			continue
		}
		previous, err := readCommand(ctx, p.RepositoryPath, "git", gitArgs(p.RepositoryPath, "cat-file", "blob", oldID)...)
		if err != nil {
			return "", "", err
		}
		oldLines := strings.Split(previous, "\n")
		newLines := strings.Split(Redact(string(content)), "\n")
		line := 0
		for line < len(oldLines) && line < len(newLines) && oldLines[line] == newLines[line] {
			line++
		}
		oldSnippet, newSnippet := "<end>", "<end>"
		if line < len(oldLines) {
			oldSnippet = oldLines[line]
		}
		if line < len(newLines) {
			newSnippet = newLines[line]
		}
		if len(oldSnippet) > 1000 {
			oldSnippet = oldSnippet[:1000]
		}
		if len(newSnippet) > 1000 {
			newSnippet = newSnippet[:1000]
		}
		excerpts = append(excerpts, fmt.Sprintf("%s — first raw differing line %d\nHEAD: %s\nWORKTREE: %s", filepath.ToSlash(file.path), line+1, oldSnippet, newSnippet))
	}
	if len(status) > 0 {
		excerpts = append(excerpts, "Comparison uses raw Git blobs without clean filters, textconv, hooks, fsmonitor, or network fetch. Line-ending normalization can appear as a change. Deleted/excluded files are not inspected. At most 8 first-difference excerpts are displayed.")
	}
	return strings.Join(status, "\n"), Redact(strings.Join(excerpts, "\n\n")), nil
}

var errorLine = regexp.MustCompile(`(?i)(\berror\b|\bfatal\b|\bpanic\b|\bexception\b|\bfailed\b|\bfailure\b|\btimeout\b|\btimed out\b|[A-Za-z0-9_]+(?:Error|Exception|Exceeded)\b)`)
var errorToken = regexp.MustCompile(`\b[A-Za-z][A-Za-z0-9_]{3,80}(?:Error|Exception|Exceeded|Failed)\b`)

func sourceMatches(ctx context.Context, p Project, inv inventory, tokens []string) ([]Evidence, []string) {
	result := []Evidence{}
	limitations := []string{}
	if len(tokens) == 0 {
		return result, limitations
	}
	if _, err := exec.LookPath("rg"); err != nil {
		return result, []string{"ripgrep 不可用，未进行日志错误标识到源码的关联搜索。"}
	}
	root, err := os.OpenRoot(p.RepositoryPath)
	if err != nil {
		return result, []string{"源码搜索无法打开安全目录。"}
	}
	defer root.Close()
	type sourceRange struct {
		path        string
		first, last int
	}
	ranges := []sourceRange{}
	var input strings.Builder
	nextLine := 1
	for _, f := range inv.files {
		b, err := readBounded(root, f.path, maxSourceBytes)
		if err != nil {
			continue
		}
		content := Redact(string(b))
		if input.Len()+len(content) > 4*1024*1024 {
			limitations = append(limitations, "源码关联搜索达到 4 MiB 输入上限。")
			break
		}
		content = strings.TrimSuffix(content, "\n") + "\n"
		count := strings.Count(content, "\n")
		ranges = append(ranges, sourceRange{f.path, nextLine, nextLine + count - 1})
		nextLine += count
		input.WriteString(content)
	}
	if len(ranges) == 0 {
		return result, limitations
	}
	for _, token := range tokens {
		args := []string{"--json", "--no-config", "--fixed-strings", "--max-count", "12", "--", token}
		raw, err := readCommandInput(ctx, p.RepositoryPath, "rg", input.String(), args...)
		if err != nil && raw == "" {
			if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 1 {
				continue
			}
			limitations = append(limitations, "源码搜索未完整执行，不能将无结果解释为没有相关实现。")
			continue
		}
		for _, line := range strings.Split(raw, "\n") {
			var event struct {
				Type string `json:"type"`
				Data struct {
					Lines struct {
						Text string `json:"text"`
					} `json:"lines"`
					LineNumber int `json:"line_number"`
				} `json:"data"`
			}
			if json.Unmarshal([]byte(line), &event) != nil || event.Type != "match" {
				continue
			}
			path := ""
			lineNumber := 0
			for _, r := range ranges {
				if event.Data.LineNumber >= r.first && event.Data.LineNumber <= r.last {
					path = r.path
					lineNumber = event.Data.LineNumber - r.first + 1
					break
				}
			}
			if path == "" {
				continue
			}
			content := strings.TrimSpace(event.Data.Lines.Text)
			if len(content) > 1000 {
				content = content[:1000]
			}
			result = append(result, Evidence{Kind: "source_match", Source: "ripgrep fixed-string search over safe bounded stdin", Summary: "日志标识 " + token + " 出现在源码；关联不等于根因", Content: Redact(content), Path: filepath.ToSlash(path), Line: lineNumber})
			if len(result) >= 12 {
				return result, limitations
			}
		}
	}
	return result, limitations
}
func evidenceID(n int) string { return "LOCAL-O" + strconv.Itoa(n) }
