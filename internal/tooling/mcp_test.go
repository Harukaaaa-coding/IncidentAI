package tooling

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

func helperConfig(t *testing.T, mode string) Config {
	return Config{Command: os.Args[0], Args: []string{"-test.run=^TestMCPHelperProcess$", "--", "mcp-helper", projectRoot(t), mode}, TimeoutMS: 3000, MaxOutputBytes: 8192}
}

func TestMCPSubprocessRoundTrip(t *testing.T) {
	client, err := NewMCP(context.Background(), helperConfig(t, "normal"))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	defs, err := client.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(defs) != 9 {
		t.Fatalf("want 9 read tools, got %d", len(defs))
	}
	for _, def := range defs {
		if def.Risk != "read" || def.Name == "apply_recovery" {
			t.Fatalf("unsafe remote tool: %+v", def)
		}
	}
	data, err := client.Call(context.Background(), "get_metrics", argsFor("deployment-regression", "rpc"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"error_rate":22`) {
		t.Fatalf("unexpected MCP metrics %s", data)
	}
	replay, err := client.Call(context.Background(), "run_reproduction", argsFor("deployment-regression", "rpc"))
	if err != nil || !strings.Contains(string(replay), `"failures":220`) {
		t.Fatalf("MCP replay failed: %s %v", replay, err)
	}
	if _, err := client.Call(context.Background(), "apply_recovery", map[string]any{"action": "rollback"}); err == nil {
		t.Fatal("remote write was accepted")
	}
	if _, err := client.Call(context.Background(), "get_metrics", argsFor("missing", "rpc")); err == nil {
		t.Fatal("MCP tool isError was ignored")
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Call(context.Background(), "get_metrics", argsFor("deployment-regression", "rpc")); err == nil {
		t.Fatal("call after close succeeded")
	}
}

func TestMCPBoundaryFailures(t *testing.T) {
	for _, mode := range []string{"malformed", "wrong-id", "wrong-version", "oversize", "missing-result", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			config := helperConfig(t, mode)
			config.TimeoutMS = 400
			start := time.Now()
			client, err := NewMCP(context.Background(), config)
			if err == nil {
				client.Close()
				t.Fatalf("accepted invalid MCP response %s", mode)
			}
			if time.Since(start) > 3*time.Second {
				t.Fatalf("failure was not bounded: %v", time.Since(start))
			}
		})
	}
	config := helperConfig(t, "normal")
	config.Tools = map[string]ToolMapping{"apply_recovery": {RemoteName: "get_metrics", Risk: "read"}}
	if _, err := NewMCP(context.Background(), config); err == nil {
		t.Fatal("operator mapping bypassed canonical write block")
	}
}

func TestMCPToolMappingAndRemoteRiskIgnored(t *testing.T) {
	config := helperConfig(t, "foreign")
	config.Tools = map[string]ToolMapping{"get_metrics": {RemoteName: "metrics.query", Risk: "read"}}
	client, err := NewMCP(context.Background(), config)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	defs, err := client.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(defs) != 1 || defs[0].Name != "get_metrics" || defs[0].Risk != "read" {
		t.Fatalf("invalid canonical mapping: %+v", defs)
	}
	data, err := client.Call(context.Background(), "get_metrics", argsFor("cache-failure", "mapped"))
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"mapped":true}` {
		t.Fatalf("mapping was not used: %s", data)
	}
}

// Re-exec the test binary to exercise real process pipes, framing and shutdown.
func TestMCPHelperProcess(t *testing.T) {
	index := -1
	for i, arg := range os.Args {
		if arg == "mcp-helper" {
			index = i
			break
		}
	}
	if index < 0 {
		return
	}
	root, mode := os.Args[index+1], os.Args[index+2]
	if mode == "normal" {
		local, err := NewLocal(root)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(2)
		}
		if err = ServeMCP(context.Background(), os.Stdin, os.Stdout, local); err != nil {
			os.Exit(3)
		}
		os.Exit(0)
	}
	scanner := bufio.NewScanner(os.Stdin)
	encoder := json.NewEncoder(os.Stdout)
	for scanner.Scan() {
		var req rpcMessage
		if json.Unmarshal(scanner.Bytes(), &req) != nil {
			os.Exit(4)
		}
		if len(req.ID) == 0 {
			continue
		}
		switch mode {
		case "malformed":
			fmt.Fprintln(os.Stdout, "not JSON")
			time.Sleep(5 * time.Second)
		case "wrong-id":
			fmt.Fprintln(os.Stdout, `{"jsonrpc":"2.0","id":999,"result":{}}`)
		case "oversize":
			fmt.Fprintln(os.Stdout, strings.Repeat("x", 16384))
			time.Sleep(5 * time.Second)
		case "missing-result":
			encoder.Encode(rpcMessage{JSONRPC: "2.0", ID: req.ID})
		case "timeout":
			time.Sleep(5 * time.Second)
		default:
			var value any
			switch req.Method {
			case "initialize":
				version := ProtocolVersion
				if mode == "wrong-version" {
					version = "1999-01-01"
				}
				value = map[string]any{"protocolVersion": version, "capabilities": map[string]any{"tools": map[string]any{}}}
			case "tools/list":
				value = map[string]any{"tools": []Definition{{Name: "metrics.query", Description: "Mapped metrics", Risk: "critical", InputSchema: map[string]any{"type": "object"}}, {Name: "apply_recovery", Risk: "read", InputSchema: map[string]any{"type": "object"}}}}
			case "tools/call":
				var params struct {
					Name string `json:"name"`
				}
				json.Unmarshal(req.Params, &params)
				value = map[string]any{"content": []map[string]string{{"type": "text", "text": `{"mapped":true}`}}, "isError": params.Name != "metrics.query"}
			}
			data, _ := json.Marshal(value)
			encoder.Encode(rpcMessage{JSONRPC: "2.0", ID: req.ID, Result: data})
		}
	}
	os.Exit(0)
}
