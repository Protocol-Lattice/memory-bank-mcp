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

	fmt.Println("Available tools:")
	for _, tool := range tools {
		fmt.Println("  -", tool.Name)
	}

	session := "refactor-session-1"
	rootOut := filepath.Join(home, "Desktop", "go-agent")
	params := map[string]any{
		"session_id": session,
		"path":       rootOut,
		"extensions": ".go,.md,.json",
	}

	// Fixed: Use the correct tool name "memory.store_codebase"
	toolName := "memory.store_codebase"

	fmt.Printf("\n🔧 Calling tool: %s\n", toolName)
	fmt.Printf("📁 Path: %s\n", rootOut)

	res, err := client.CallTool(ctx, toolName, params)
	if err != nil {
		log.Fatalf("❌ Failed to call tool: %v", err)
	}

	fmt.Println("\n✅ Codebase stored successfully!")
	fmt.Printf("📄 Result: %+v\n", res)

	fmt.Println("\n🔄 Starting refactoring process...")

	refactorParams := map[string]any{
		"session_id":   session,
		"query":        "main function and tool calling logic",
		"instructions": "Improve code structure, add error handling, and make it more maintainable. Add comments explaining key sections.",
		"root_path":    filepath.Join(home, "Desktop", "go-agent"),
		"limit":        10,
	}

	refactorToolName := "memory.apply_refactor"
	fmt.Printf("🔧 Calling tool: %s\n", refactorToolName)
	fmt.Printf("📝 Query: %s\n", refactorParams["query"])
	fmt.Printf("📁 Output path: %s\n", refactorParams["root_path"])

	refactorRes, err := client.CallTool(ctx, refactorToolName, refactorParams)
	if err != nil {
		log.Fatalf("❌ Failed to call refactor tool: %v", err)
	}

	fmt.Println("\n✅ Refactoring completed successfully!")
	fmt.Printf("📄 Refactor Result: %+v\n", refactorRes)
}
