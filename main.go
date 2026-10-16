package main

import (
	"context"
	"encoding/json"
	"flag"
	"log"
	"os"
	"path/filepath"

	"IncidentAI/internal/platform"
	"IncidentAI/internal/tooling"
)

func main() {
	eval := flag.Bool("eval", false, "run 60-case offline benchmark and ablations, output JSON")
	probe := flag.Bool("check-model", false, "make a small request to the configured model")
	flag.Parse()
	root, err := filepath.Abs(".")
	if err != nil {
		log.Fatal(err)
	}
	if *eval {
		report, err := platform.RunEvaluation(context.Background(), root)
		if err != nil {
			log.Fatal(err)
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(report)
		return
	}
	model, err := platform.LoadModel(root)
	if err != nil {
		log.Fatal(err)
	}
	if *probe {
		if model == nil {
			log.Fatal("no model configured")
		}
		raw, usage, err := model.Complete(context.Background(), "Return JSON only: {\"status\":\"ok\"}. This is a connectivity check.", "Verify the chat-completions response format.", 32)
		if err != nil {
			log.Fatal(err)
		}
		_ = json.NewEncoder(os.Stdout).Encode(map[string]any{"provider": model.Provider, "model": model.Name, "result": raw, "usage": usage})
		return
	}
	local, err := tooling.NewLocal(root)
	if err != nil {
		log.Fatal(err)
	}
	var backend tooling.Backend = local
	if path := os.Getenv("INCIDENTAI_MCP_CONFIG"); path != "" {
		var cfg tooling.Config
		data, err := os.ReadFile(path)
		if err != nil {
			log.Fatal(err)
		}
		if err = json.Unmarshal(data, &cfg); err != nil {
			log.Fatal(err)
		}
		remote, err := tooling.NewMCP(context.Background(), cfg)
		if err != nil {
			log.Fatal(err)
		}
		backend = remote
	}
	defer backend.Close()
	dataDir := os.Getenv("INCIDENTAI_DATA_DIR")
	if dataDir == "" {
		dataDir = filepath.Join(root, "data")
	}
	engine, err := platform.NewEngine(root, dataDir, backend, model, local.Scenarios())
	if err != nil {
		log.Fatal(err)
	}
	address := os.Getenv("INCIDENTAI_ADDR")
	if address == "" {
		address = "127.0.0.1:8099"
	}
	if model == nil {
		log.Print("IncidentAI V2 · offline rule baseline")
	} else {
		log.Printf("IncidentAI V2 · %s / %s", model.Provider, model.Name)
	}
	log.Printf("Control center: http://%s", address)
	log.Fatal(platform.HTTPServer(address, platform.NewServer(engine).Handler()).ListenAndServe())
}
