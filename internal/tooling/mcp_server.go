package tooling

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
)

// ServeMCP exports read-only fixture tools over newline-delimited JSON-RPC.
// Recovery is never listed or callable, even when the supplied Backend has it.
func ServeMCP(ctx context.Context, input io.Reader, output io.Writer, backend Backend) error {
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 4096), 1024*1024)
	encoder := json.NewEncoder(output)
	initialized, ready := false, false
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return err
		}
		var request rpcMessage
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			if err := encoder.Encode(map[string]any{"jsonrpc": "2.0", "id": nil, "error": rpcError{-32700, "invalid JSON"}}); err != nil {
				return err
			}
			continue
		}
		if len(request.ID) == 0 {
			if initialized && request.Method == "notifications/initialized" {
				ready = true
			}
			continue
		}
		response := rpcMessage{JSONRPC: "2.0", ID: request.ID}
		var value any
		var callErr error
		if request.JSONRPC != "2.0" {
			response.Error = &rpcError{-32600, "expected JSON-RPC 2.0"}
		} else {
			switch request.Method {
			case "initialize":
				if initialized {
					response.Error = &rpcError{-32600, "already initialized"}
					break
				}
				var params struct {
					ProtocolVersion string `json:"protocolVersion"`
				}
				if json.Unmarshal(request.Params, &params) != nil || params.ProtocolVersion == "" {
					response.Error = &rpcError{-32602, "missing protocolVersion"}
					break
				}
				initialized = true
				value = map[string]any{"protocolVersion": ProtocolVersion, "capabilities": map[string]any{"tools": map[string]any{"listChanged": false}}, "serverInfo": map[string]any{"name": "IncidentAI read-only simulator", "version": "2.0.0"}}
			case "ping":
				value = map[string]any{}
			case "tools/list":
				if !ready {
					response.Error = &rpcError{-32000, "initialization incomplete"}
					break
				}
				defs, err := backend.List(ctx)
				callErr = err
				reads := make([]map[string]any, 0)
				for _, def := range defs {
					if readNames[def.Name] && def.Risk == "read" {
						reads = append(reads, map[string]any{"name": def.Name, "description": def.Description, "inputSchema": def.InputSchema, "annotations": map[string]any{"readOnlyHint": true, "destructiveHint": false}})
					}
				}
				value = map[string]any{"tools": reads}
			case "tools/call":
				if !ready {
					response.Error = &rpcError{-32000, "initialization incomplete"}
					break
				}
				var params struct {
					Name      string         `json:"name"`
					Arguments map[string]any `json:"arguments"`
				}
				if json.Unmarshal(request.Params, &params) != nil || !readNames[params.Name] {
					response.Error = &rpcError{-32602, "unknown or forbidden tool"}
					break
				}
				data, err := backend.Call(ctx, params.Name, params.Arguments)
				text := string(data)
				if err != nil {
					text = err.Error()
				}
				value = map[string]any{"content": []map[string]any{{"type": "text", "text": text}}, "isError": err != nil}
			default:
				response.Error = &rpcError{-32601, "method not found"}
			}
		}
		if callErr != nil {
			response.Error = &rpcError{-32603, callErr.Error()}
		}
		if response.Error == nil {
			encoded, err := json.Marshal(value)
			if err != nil {
				return err
			}
			response.Result = encoded
		}
		if err := encoder.Encode(response); err != nil {
			return err
		}
	}
	if err := scanner.Err(); err != nil {
		return errors.New("MCP request exceeds limit or input failed: " + err.Error())
	}
	return nil
}
