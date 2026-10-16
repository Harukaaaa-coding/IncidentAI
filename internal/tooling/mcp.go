package tooling

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"
)

const ProtocolVersion = "2025-03-26"

// Config is trusted operator configuration, never model-generated tool input.
// Canonical tool names map to remote names; only locally declared reads are allowed.
type Config struct {
	Command        string                 `json:"command"`
	Args           []string               `json:"args"`
	Dir            string                 `json:"dir,omitempty"`
	TimeoutMS      int                    `json:"timeout_ms,omitempty"`
	MaxOutputBytes int                    `json:"max_output_bytes,omitempty"`
	Tools          map[string]ToolMapping `json:"tools,omitempty"`
}
type ToolMapping struct {
	RemoteName string `json:"remote_name"`
	Risk       string `json:"risk"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}
type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}
type incoming struct {
	message rpcMessage
	err     error
}

type MCP struct {
	config    Config
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	messages  chan incoming
	done      chan struct{}
	waited    chan struct{}
	gate      chan struct{}
	closeOnce sync.Once
	sequence  int
	allowed   map[string]Definition
	remote    map[string]string
}

func NewMCP(ctx context.Context, config Config) (*MCP, error) {
	if strings.TrimSpace(config.Command) == "" {
		return nil, errors.New("MCP command is required")
	}
	if config.TimeoutMS == 0 {
		config.TimeoutMS = 10000
	}
	if config.TimeoutMS < 1 || config.TimeoutMS > 120000 {
		return nil, errors.New("MCP timeout must be between 1 and 120000 ms")
	}
	if config.MaxOutputBytes == 0 {
		config.MaxOutputBytes = 1024 * 1024
	}
	if config.MaxOutputBytes < 1024 || config.MaxOutputBytes > 4*1024*1024 {
		return nil, errors.New("MCP output limit must be between 1 KiB and 4 MiB")
	}
	for name, mapping := range config.Tools {
		if !readNames[name] || mapping.Risk != "read" || mapping.RemoteName == "" {
			return nil, fmt.Errorf("MCP mapping %q must be a known local read with explicit risk=read", name)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	cmd := exec.Command(config.Command, config.Args...)
	cmd.Dir = config.Dir
	cmd.Stderr = io.Discard
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		stdin.Close()
		stdout.Close()
		return nil, fmt.Errorf("start MCP server: %w", err)
	}
	m := &MCP{config: config, cmd: cmd, stdin: stdin, messages: make(chan incoming, 8), done: make(chan struct{}), waited: make(chan struct{}), gate: make(chan struct{}, 1), allowed: map[string]Definition{}, remote: map[string]string{}}
	m.gate <- struct{}{}
	go m.read(stdout)
	go func() { _ = cmd.Wait(); close(m.waited) }()
	result, err := m.request(ctx, "initialize", map[string]any{"protocolVersion": ProtocolVersion, "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "IncidentAI", "version": "2.0.0"}})
	if err != nil {
		m.Close()
		return nil, err
	}
	var initialized struct {
		ProtocolVersion string                     `json:"protocolVersion"`
		Capabilities    map[string]json.RawMessage `json:"capabilities"`
	}
	if err = json.Unmarshal(result, &initialized); err != nil || initialized.ProtocolVersion != ProtocolVersion {
		m.Close()
		return nil, errors.New("MCP server returned an unsupported initialization response")
	}
	if _, ok := initialized.Capabilities["tools"]; !ok {
		m.Close()
		return nil, errors.New("MCP server does not support tools")
	}
	if err = m.write(map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}); err != nil {
		m.Close()
		return nil, err
	}
	if _, err = m.List(ctx); err != nil {
		m.Close()
		return nil, err
	}
	return m, nil
}

func (m *MCP) read(reader io.Reader) {
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 4096), m.config.MaxOutputBytes)
	for scanner.Scan() {
		var msg rpcMessage
		err := json.Unmarshal(scanner.Bytes(), &msg)
		if err == nil && msg.JSONRPC != "2.0" {
			err = errors.New("MCP response missing JSON-RPC 2.0")
		}
		select {
		case m.messages <- incoming{msg, err}:
		case <-m.done:
			return
		}
		if err != nil {
			return
		}
	}
	err := scanner.Err()
	if err == nil {
		err = io.EOF
	}
	select {
	case m.messages <- incoming{err: err}:
	case <-m.done:
	}
}

func (m *MCP) write(value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if len(data) > m.config.MaxOutputBytes {
		return errors.New("MCP request exceeds output limit")
	}
	_, err = m.stdin.Write(append(data, '\n'))
	return err
}

func (m *MCP) request(ctx context.Context, method string, params any) (json.RawMessage, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(m.config.TimeoutMS)*time.Millisecond)
	defer cancel()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-m.done:
		return nil, errors.New("MCP client is closed")
	case <-m.gate:
	}
	defer func() { m.gate <- struct{}{} }()
	m.sequence++
	id := m.sequence
	// Write on a goroutine so even a peer that stops reading stdin obeys timeout.
	written := make(chan error, 1)
	go func() {
		written <- m.write(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	}()
	select {
	case err := <-written:
		if err != nil {
			return nil, fmt.Errorf("MCP write: %w", err)
		}
	case <-ctx.Done():
		m.Close()
		return nil, ctx.Err()
	case <-m.done:
		return nil, errors.New("MCP client is closed")
	}
	for notifications := 0; notifications < 128; notifications++ {
		select {
		case <-ctx.Done():
			m.Close()
			return nil, fmt.Errorf("MCP %s: %w", method, ctx.Err())
		case <-m.done:
			return nil, errors.New("MCP client is closed")
		case input := <-m.messages:
			if input.err != nil {
				m.Close()
				return nil, fmt.Errorf("MCP read: %w", input.err)
			}
			msg := input.message
			if msg.Method != "" {
				if len(msg.ID) > 0 {
					m.Close()
					return nil, errors.New("unsupported MCP server request: no client capabilities were advertised")
				}
				continue
			}
			var responseID int
			if json.Unmarshal(msg.ID, &responseID) != nil || responseID != id {
				m.Close()
				return nil, errors.New("MCP response ID mismatch")
			}
			if msg.Error != nil {
				return nil, fmt.Errorf("MCP %s error %d: %s", method, msg.Error.Code, msg.Error.Message)
			}
			if len(msg.Result) == 0 || string(msg.Result) == "null" {
				m.Close()
				return nil, errors.New("MCP response is missing result")
			}
			return msg.Result, nil
		}
	}
	m.Close()
	return nil, errors.New("MCP notification flood limit exceeded")
}

func (m *MCP) List(ctx context.Context) ([]Definition, error) {
	// Tool discovery is deliberately separate from authorization. Remote risk
	// annotations and tool names cannot authorize production mutations.
	discovered := map[string]Definition{}
	cursor := ""
	for page := 0; page < 20; page++ {
		params := map[string]any{}
		if cursor != "" {
			params["cursor"] = cursor
		}
		data, err := m.request(ctx, "tools/list", params)
		if err != nil {
			return nil, err
		}
		var result struct {
			Tools      []Definition `json:"tools"`
			NextCursor string       `json:"nextCursor"`
		}
		if err = json.Unmarshal(data, &result); err != nil {
			return nil, fmt.Errorf("MCP tools/list: %w", err)
		}
		if result.Tools == nil {
			return nil, errors.New("MCP tools/list omitted tools")
		}
		for _, def := range result.Tools {
			if def.Name == "" || def.InputSchema == nil {
				return nil, errors.New("MCP returned malformed tool definition")
			}
			discovered[def.Name] = def
		}
		if result.NextCursor == "" {
			break
		}
		if result.NextCursor == cursor || page == 19 {
			return nil, errors.New("MCP tool pagination limit exceeded")
		}
		cursor = result.NextCursor
	}
	allowed := map[string]Definition{}
	remote := map[string]string{}
	for _, local := range definitions() {
		if !readNames[local.Name] {
			continue
		}
		remoteName := local.Name
		if len(m.config.Tools) > 0 {
			mapping, ok := m.config.Tools[local.Name]
			if !ok {
				continue
			}
			remoteName = mapping.RemoteName
		}
		if def, ok := discovered[remoteName]; ok {
			local.Description = def.Description
			allowed[local.Name] = local
			remote[local.Name] = remoteName
		}
	}
	// List is normally startup-only, but use the same gate to make refresh safe.
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-m.gate:
	}
	m.allowed = allowed
	m.remote = remote
	m.gate <- struct{}{}
	result := make([]Definition, 0, len(allowed))
	for _, def := range allowed {
		result = append(result, def)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Name < result[j].Name })
	return result, nil
}

func (m *MCP) Call(ctx context.Context, name string, args map[string]any) (json.RawMessage, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-m.gate:
	}
	remoteName, ok := m.remote[name]
	m.gate <- struct{}{}
	if !ok || !readNames[name] {
		return nil, fmt.Errorf("MCP tool %q is not an authorized local read", name)
	}
	data, err := m.request(ctx, "tools/call", map[string]any{"name": remoteName, "arguments": args})
	if err != nil {
		return nil, err
	}
	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		Structured json.RawMessage `json:"structuredContent"`
		IsError    bool            `json:"isError"`
	}
	if err = json.Unmarshal(data, &result); err != nil {
		return nil, fmt.Errorf("MCP tools/call: %w", err)
	}
	texts := make([]string, 0)
	for _, content := range result.Content {
		if content.Type == "text" {
			texts = append(texts, content.Text)
		}
	}
	joined := strings.Join(texts, "\n")
	if result.IsError {
		return nil, fmt.Errorf("MCP tool %s failed: %.1000s", name, joined)
	}
	if len(result.Structured) > 0 && string(result.Structured) != "null" {
		return result.Structured, nil
	}
	if len(texts) == 0 {
		return nil, errors.New("MCP tool returned no usable text or structured result")
	}
	if json.Valid([]byte(joined)) {
		return json.RawMessage(joined), nil
	}
	return json.Marshal(map[string]any{"text": joined})
}

func (m *MCP) Close() error {
	m.closeOnce.Do(func() {
		close(m.done)
		_ = m.stdin.Close()
		select {
		case <-m.waited:
			return
		case <-time.After(500 * time.Millisecond):
		}
		_ = m.cmd.Process.Kill()
		select {
		case <-m.waited:
		case <-time.After(time.Second):
		}
	})
	return nil
}
