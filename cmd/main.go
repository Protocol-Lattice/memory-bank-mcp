package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"

	utcp "github.com/universal-tool-calling-protocol/go-utcp"
)

func main() {
	ctx := context.Background()

	// --- UTCP client setup ---
	home, err := os.UserHomeDir()
	if err != nil {
		log.Fatalf("failed to resolve home directory: %v", err)
	}

	cfg := &utcp.UtcpClientConfig{
		ProvidersFilePath: filepath.Join(home, "utcp", "provider.json"),
	}

	client, err := utcp.NewUTCPClient(ctx, cfg, nil, nil)
	if err != nil {
		log.Fatalf("failed to create UTCP client: %v", err)
	}
	tools, err := client.SearchTools("", 40)
	if err != nil {
		log.Fatalf("❌ Failed to search tools: %v", err)
	}

	for _, tool := range tools {
		fmt.Println(tool.Name)
	}

	session := "refactor-session"
	rootOut := filepath.Join(home, "Desktop", "go-utcp")
	res, err := client.CallTool(ctx, "memory-http.store_codebase", map[string]any{
		"session_id": session,
		"path":       rootOut,
		"extensions": ".go,.md,.json",
	})

	if err != nil {
		log.Fatalf("❌ Store codebase failed: %v", err)
	}

	fmt.Println("✅ Codebase stored successfully!")
	fmt.Printf("📄 Stored files: %v\n", res)

	// --- 2. Apply refactor ---
	fmt.Println("🛠  Applying refactor using memory.apply_refactor...")
	refactorRes, err := client.CallTool(ctx, "memory-http.apply_refactor", map[string]any{
		"session_id":   session,
		"query":        "Refactor code for maintainability",
		"instructions": "Use idiomatic Go and improve modularity",
		"root_path":    rootOut,
		"limit":        120,
	})

	if err != nil {
		log.Fatalf("❌ Refactor failed: %v", err)
	}

	fmt.Println("✅ Refactor applied successfully!")
	fmt.Printf("📄 Written files: %v\n", refactorRes)
}
