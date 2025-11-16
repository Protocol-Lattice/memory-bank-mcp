// main.go
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Protocol-Lattice/go-agent/src/memory"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// GeminiSettings represents the configuration from .gemini/settings.json
type GeminiSettings struct {
	LLMModel         string `json:"llm_model"`
	MemoryStore      string `json:"memory_store"`
	QdrantURL        string `json:"qdrant_url"`
	QdrantCollection string `json:"qdrant_collection"`
	QdrantAPIKey     string `json:"qdrant_api_key"`
	PostgresDSN      string `json:"postgres_dsn"`
	MongoURI         string `json:"mongo_uri"`
	MongoDatabase    string `json:"mongo_database"`
	MongoCollection  string `json:"mongo_collection"`
	ShortTermSize    int    `json:"short_term_size"`
	DefaultSpaceTTL  int    `json:"default_space_ttl_sec"`
}

// App wires MemoryBank + SessionMemory + Spaces and registers MCP tools.
type App struct {
	bank   *memory.MemoryBank
	sm     *memory.SessionMemory
	engine *memory.Engine
	spaces *memory.SpaceRegistry
	shared map[string]*memory.SharedSession
	mu     sync.RWMutex
}

// loadGeminiSettings reads configuration from .gemini/settings.json
func loadGeminiSettings() (*GeminiSettings, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("failed to get home directory: %w", err)
	}

	settingsPath := filepath.Join(home, ".gemini", "settings.json")

	var settings GeminiSettings
	if data, err := os.ReadFile(settingsPath); err == nil {
		if err := json.Unmarshal(data, &settings); err != nil {
			return nil, fmt.Errorf("failed to parse settings JSON: %w", err)
		}
		log.Printf("Loaded settings from %s", settingsPath)
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("failed to read settings file: %w", err)
	} else {
		log.Printf("No .gemini/settings.json found, using defaults")
	}
	if settings.MemoryStore == "" {
		settings.MemoryStore = "qdrant"
	}
	if settings.QdrantURL == "" {
		settings.QdrantURL = "http://localhost:6333"
	}
	if settings.QdrantCollection == "" {
		settings.QdrantCollection = "memories"
	}
	if settings.ShortTermSize == 0 {
		settings.ShortTermSize = 500000
	}
	if settings.DefaultSpaceTTL == 0 {
		settings.DefaultSpaceTTL = 86400
	}
	// Add similar checks for other fields like QdrantAPIKey, PostgresDSN, etc., if needed

	return &settings, nil
}
func newApp(ctx context.Context, settings *GeminiSettings) (*App, error) {
	// Use settings with environment variable fallbacks
	storeKind := strings.ToLower(envOrDefault("MEMORY_STORE", settings.MemoryStore))
	shortBuf := envIntOrDefault("SHORT_TERM_SIZE", settings.ShortTermSize)
	spaceTTL := envIntOrDefault("DEFAULT_SPACE_TTL_SEC", settings.DefaultSpaceTTL)

	var vs memory.VectorStore
	var err error

	switch storeKind {
	case "postgres", "pg":
		dsn := envOrDefault("POSTGRES_DSN", settings.PostgresDSN)
		if dsn == "" {
			return nil, fmt.Errorf("postgres_dsn not configured")
		}
		vs, err = memory.NewPostgresStore(ctx, dsn)
		if err != nil {
			return nil, fmt.Errorf("failed to create postgres store: %w", err)
		}

	case "qdrant":
		base := envOrDefault("QDRANT_URL", settings.QdrantURL)
		col := envOrDefault("QDRANT_COLLECTION", settings.QdrantCollection)
		api := envOrDefault("QDRANT_API_KEY", settings.QdrantAPIKey)
		vs = memory.NewQdrantStore(base, col, api)

	case "mongo":
		uri := envOrDefault("MONGO_URI", settings.MongoURI)
		database := envOrDefault("MONGO_DATABASE", settings.MongoDatabase)
		collection := envOrDefault("MONGO_COLLECTION", settings.MongoCollection)
		if uri == "" || database == "" {
			return nil, fmt.Errorf("mongo_uri and mongo_database must be configured")
		}
		vs, err = memory.NewMongoStore(ctx, uri, database, collection)
		if err != nil {
			return nil, fmt.Errorf("failed to create mongo store: %w", err)
		}

	default:
		log.Printf("Using in-memory store (store kind: %s)", storeKind)
		vs = memory.NewInMemoryStore()
	}

	bank := memory.NewMemoryBankWithStore(vs)
	eng := memory.NewEngine(vs, memory.DefaultOptions()).WithEmbedder(memory.AutoEmbedder())
	sm := memory.NewSessionMemory(bank, shortBuf).WithEmbedder(memory.AutoEmbedder()).WithEngine(eng)
	spaces := memory.NewSpaceRegistry(time.Duration(spaceTTL) * time.Second)

	return &App{
		bank:   bank,
		sm:     sm,
		engine: eng,
		spaces: spaces,
		shared: make(map[string]*memory.SharedSession),
	}, nil
}

func (a *App) sharedFor(principal string) *memory.SharedSession {
	a.mu.Lock()
	defer a.mu.Unlock()
	ss := a.shared[principal]
	if ss == nil {
		ss = memory.NewSharedSession(a.sm, principal)
		a.shared[principal] = ss
	}
	return ss
}

func getStringParam(req mcp.CallToolRequest, key string) string {
	args, ok := req.Params.Arguments.(map[string]interface{})
	if !ok {
		return ""
	}
	val, ok := args[key]
	if !ok {
		return ""
	}
	str, _ := val.(string)
	return str
}

func getNumberParam(req mcp.CallToolRequest, key string) float64 {
	args, ok := req.Params.Arguments.(map[string]interface{})
	if !ok {
		return 0
	}
	val, ok := args[key]
	if !ok {
		return 0
	}
	num, _ := val.(float64)
	return num
}

func main() {
	var (
		transport = flag.String("transport", "stdio", "stdio|http")
		addr      = flag.String("addr", ":8080", "addr for http")
	)

	flag.Parse()

	ctx := context.Background()

	// Load settings from .gemini/settings.json
	settings, err := loadGeminiSettings()
	if err != nil {
		log.Fatalf("Failed to load settings: %v", err)
	}

	app, err := newApp(ctx, settings)
	if err != nil {
		log.Fatal(err)
	}

	// Use settings with environment variable overrides
	llmModel := envOrDefault("LLM_MODEL", "gemini-2.5-pro")
	qdrantURL := envOrDefault("QDRANT_URL", settings.QdrantURL)
	qdrantCollection := envOrDefault("QDRANT_COLLECTION", settings.QdrantCollection)

	log.Printf("Configuration: LLM=%s, Store=%s, Qdrant=%s/%s",
		llmModel, settings.MemoryStore, qdrantURL, qdrantCollection)

	s := server.NewMCPServer(
		"memory-bank",
		"0.1.0",
		server.WithToolCapabilities(true),
		server.WithRecovery(),
	)

	// ---- Tool: health.ping ----
	ping := mcp.NewTool("health.ping", mcp.WithDescription("Return pong and server time"))
	s.AddTool(ping, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		res, _ := mcp.NewToolResultJSON(map[string]any{"pong": true, "time": time.Now().Format(time.RFC3339)})
		return res, nil
	})

	// ---- Tool: memory.embed(text) -> []float32 ----
	embedTool := mcp.NewTool("embed",
		mcp.WithDescription("Return embedding vector for a text using AutoEmbedder (ADK_EMBED_* env)"),
		mcp.WithString("text", mcp.Required(), mcp.Description("Text to embed")),
	)
	s.AddTool(embedTool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		text, err := req.RequireString("text")
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("missing required parameter 'text': %v", err)), nil
		}
		e, err := app.sm.Embed(ctx, text)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		res, _ := mcp.NewToolResultJSON(map[string]any{
			"embedding": e,
		})
		return res, nil
	})

	initTool := mcp.NewTool("initialize",
		mcp.WithDescription("Generate a new session ID and save it to disk"))
	s.AddTool(initTool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		sid := fmt.Sprintf("sess-%d", time.Now().UnixNano())

		// Save the session ID to disk
		if err := saveSessionID(sid); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to save session: %v", err)), nil
		}

		res, _ := mcp.NewToolResultJSON(map[string]any{
			"session_id": sid,
			"saved":      true,
		})
		return res, nil
	})

	// Tool: prompt_with_memories - Enhanced version that uses stored session
	promptWithMemories := mcp.NewTool("prompt_with_memories",
		mcp.WithDescription("Build a prompt augmented with relevant memories from the session"),
		mcp.WithString("session_id", mcp.Description("Session ID (optional, will use stored session if not provided)")),
		mcp.WithString("query", mcp.Required(), mcp.Description("The user's query/prompt")),
		mcp.WithNumber("limit", mcp.Description("Number of relevant memories to retrieve (default 5)")),
		mcp.WithBoolean("include_short_term", mcp.Description("Include short-term memories (default true)")),
	)
	s.AddTool(promptWithMemories, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		// Get session ID - use provided or load from disk
		sid := getStringParam(req, "session_id")
		if sid == "" {
			loadedSID, err := loadSessionID()
			if err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("failed to load session: %v", err)), nil
			}
			if loadedSID == "" {
				return mcp.NewToolResultError("no session_id provided and no saved session found. Use initialize or get_or_create_session first"), nil
			}
			sid = loadedSID
		}

		query, err := req.RequireString("query")
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("missing query: %v", err)), nil
		}

		limit := int(getNumberParam(req, "limit"))
		if limit <= 0 {
			limit = 5
		}

		// Retrieve relevant memories
		memories, err := app.sm.RetrieveContext(ctx, sid, query, limit)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to retrieve memories: %v", err)), nil
		}

		// Build augmented prompt
		var promptBuilder strings.Builder

		if len(memories) > 0 {
			promptBuilder.WriteString("# Relevant Context from Memory\n\n")
			for i, mem := range memories {
				promptBuilder.WriteString(fmt.Sprintf("## Memory %d (Score: %.3f)\n", i+1, mem.Score))
				promptBuilder.WriteString(fmt.Sprintf("%s\n\n", mem.Content))

				// Include metadata if present
				if len(mem.Metadata) > 0 {
					metaJSON, _ := json.MarshalIndent(mem.Metadata, "", "  ")
					promptBuilder.WriteString(fmt.Sprintf("Metadata: %s\n\n", string(metaJSON)))
				}
			}
			promptBuilder.WriteString("---\n\n")
		}

		promptBuilder.WriteString("# User Query\n\n")
		promptBuilder.WriteString(query)

		result := map[string]any{
			"session_id":       sid,
			"query":            query,
			"memories_found":   len(memories),
			"augmented_prompt": promptBuilder.String(),
			"memories":         memories,
		}

		res, _ := mcp.NewToolResultJSON(result)
		return res, nil
	})

	// ---- Tool: agent_mode.set_prompt ----
	agentModeSet := mcp.NewTool(
		"agent_mode.set_prompt",
		mcp.WithDescription("Store or return the Memory-Aware Agent Mode system prompt"),
		mcp.WithString("session_id", mcp.Required(), mcp.Description("Session to store the agent-mode prompt into")),
		mcp.WithBoolean("store", mcp.Description("If true, store the prompt in long-term memory")),
	)

	s.AddTool(agentModeSet, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {

		sid, _ := req.RequireString("session_id")
		storeFlag := req.GetBool("store", false)

		promptText := `Before answering any user query, you will always be given an “augmented prompt” that contains:
- the user’s original query
- the retrieved memory records
- metadata associated with those memories

Your primary goal is to answer the user's query directly.

Your behavior:

1. Always incorporate the memory block into your understanding of the current request.
2. Use the retrieved context to actively influence how you interpret the user’s new prompt, including their intent, preferences, past decisions, and ongoing tasks.
3. Interpret user queries through the lens of these retrieved memories.
4. Naturally weave relevant memory information into your reasoning and your final answer.
5. After receiving the augmented prompt, your next step should be to generate a helpful, user-facing response, not to call another tool unless the user's query explicitly asks for a tool-based action (such as searching, retrieving, saving, or modifying content).
6. If the memory contradicts the user’s new request, follow the new request but use memory to refine, clarify, or adapt the response.
7. Do not reveal, mention, or describe anything about retrieval mechanisms (vector search, embeddings, memory-bank, session tools, etc.).
8. Assume memory is incomplete; infer intent conservatively.
9. Every answer must be shaped by both the current user query and the retrieved memories included with it.
10. Never create or request a new session ID if one already exists; always reuse the active session.

The memory block is always part of your input. Treat it as authoritative context for all answers.
`

		// Optionally store it as long-term memory
		if storeFlag {
			meta := map[string]any{
				"type":  "agent_mode_prompt",
				"scope": "system",
			}

			rec, err := app.engine.Store(ctx, sid, promptText, meta)
			if err != nil {
				return mcp.NewToolResultError(err.Error()), nil
			}
			return mcp.NewToolResultJSON(map[string]any{
				"status":  "stored",
				"session": sid,
				"record":  rec,
			})
		}

		// Otherwise just return the prompt
		res, _ := mcp.NewToolResultJSON(map[string]any{
			"prompt": promptText,
			"stored": false,
		})
		return res, nil
	})

	// ---- Tool: memory.add_short ----
	addShort := mcp.NewTool("add_short",
		mcp.WithDescription("Append short-term memory to a session buffer (flush later to long-term)"),
		mcp.WithString("session_id", mcp.Required()),
		mcp.WithString("content", mcp.Required()),
		mcp.WithString("metadata_json", mcp.Description("JSON object (string->string)")),
	)
	s.AddTool(addShort, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		sid, err := req.RequireString("session_id")
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("missing session_id: %v", err)), nil
		}
		content, err := req.RequireString("content")
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("missing content: %v", err)), nil
		}

		metaStr := getStringParam(req, "metadata_json")
		m := map[string]string{}
		if metaStr != "" {
			if err := json.Unmarshal([]byte(metaStr), &m); err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("invalid metadata_json: %v", err)), nil
			}
		}
		e, err := app.sm.Embed(ctx, content)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		app.sm.AddShortTerm(sid, content, stringMapToJSON(m), e)
		return mcp.NewToolResultText("ok"), nil
	})

	// ---- Tool: memory.flush ----
	flush := mcp.NewTool("flush",
		mcp.WithDescription("Promote short-term buffer to long-term vector store for the given session"),
		mcp.WithString("session_id", mcp.Required()),
	)
	s.AddTool(flush, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		sid, err := req.RequireString("session_id")
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("missing session_id: %v", err)), nil
		}
		if err := app.sm.FlushToLongTerm(ctx, sid); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText("flushed"), nil
	})

	// ---- Tool: memory.store_long ----
	storeLong := mcp.NewTool("store_long",
		mcp.WithDescription("Embed, score and persist a long-term memory (uses Engine)"),
		mcp.WithString("session_id", mcp.Required()),
		mcp.WithString("content", mcp.Required()),
		mcp.WithString("metadata_json", mcp.Description("JSON object (any) e.g. {\"source\":\"chat\"}")),
	)
	s.AddTool(storeLong, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		sid, err := req.RequireString("session_id")
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("missing session_id: %v", err)), nil
		}
		content, err := req.RequireString("content")
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("missing content: %v", err)), nil
		}

		meta := map[string]any{}
		metaStr := getStringParam(req, "metadata_json")
		if metaStr != "" {
			if err := json.Unmarshal([]byte(metaStr), &meta); err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("invalid metadata_json: %v", err)), nil
			}
		}

		rec, err := app.engine.Store(ctx, sid, content, meta)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		res, _ := mcp.NewToolResultJSON(rec)
		return res, nil
	})

	// Tool: retrieve_context
	retrieveCtx := mcp.NewTool(
		"memory.retrieve_context",
		mcp.WithDescription("Retrieve relevant memory records based on a semantic query."),
		mcp.WithString("session_id", mcp.Required()),
		mcp.WithString("query", mcp.Required()),
		mcp.WithNumber("limit", mcp.Description("Number of records to return (default 3)")),
		mcp.WithBoolean("all", mcp.Description("If true, returns all stored items regardless of similarity")),
	)
	s.AddTool(retrieveCtx, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		sessionID, _ := req.RequireString("session_id")
		query, _ := req.RequireString("query")
		limit := int(req.GetInt("limit", 3))
		log.Printf("[memory-bank] retrieve_context: session=%s query=%s limit=%d\n", sessionID, query, limit)

		recs, err := app.sm.RetrieveContext(ctx, sessionID, query, limit)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		return mcp.NewToolResultJSON(map[string]any{
			"session_id": sessionID,
			"query":      query,
			"limit":      limit,
			"results":    recs,
		})
	})

	// Tool: memory_query
	memoryQuery := mcp.NewTool(
		"memory.query",
		mcp.WithDescription("Perform a semantic search/vector search on the content in the memory."),
		mcp.WithString("session_id", mcp.Required()),
		mcp.WithString("query", mcp.Required()),
		mcp.WithNumber("limit", mcp.Description("Number of records to return (default 10)")),
	)
	s.AddTool(memoryQuery, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		sessionID, _ := req.RequireString("session_id")
		query, _ := req.RequireString("query")
		limit := int(req.GetInt("limit", 10))
		log.Printf("[memory-bank] memory_query: session=%s query=%s limit=%d\n", sessionID, query, limit)

		recs, err := app.sm.RetrieveContext(ctx, sessionID, query, limit)
		if err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}

		return mcp.NewToolResultJSON(map[string]any{
			"session_id": sessionID,
			"query":      query,
			"limit":      limit,
			"results":    recs,
		})
	})

	chainPrompt := mcp.NewTool("chain_prompt",
		mcp.WithDescription("Embed, store, flush, retrieve, and return a prompt with context memories"),
		mcp.WithString("session_id", mcp.Required(), mcp.Description("Memory session identifier")),
		mcp.WithString("content", mcp.Required(), mcp.Description("New content to store and embed")),
		mcp.WithString("query", mcp.Required(), mcp.Description("Semantic query for retrieval")),
		mcp.WithNumber("limit", mcp.Description("Number of memories to retrieve")),
		mcp.WithString("include_contents", mcp.Description("Include raw file contents in prompt output")),
	)

	s.AddTool(chainPrompt, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		sid, _ := req.RequireString("session_id")
		content, _ := req.RequireString("content")
		query, _ := req.RequireString("query")

		limit := int(getNumberParam(req, "limit"))
		if limit <= 0 {
			limit = 5
		}

		e, err := app.sm.Embed(ctx, content)
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("embed failed: %v", err)), nil
		}

		app.sm.AddShortTerm(sid, content, "{}", e)

		if err := app.sm.FlushToLongTerm(ctx, sid); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("flush failed: %v", err)), nil
		}

		if _, err := app.sm.RetrieveContext(ctx, sid, query, limit); err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("retrieve failed: %v", err)), nil
		}

		out := map[string]any{
			"status":   "Completed",
			"session":  sid,
			"query":    query,
			"embedded": len(e) > 0,
		}

		res, _ := mcp.NewToolResultJSON(out)
		return res, nil
	})

	// ---- Tools: spaces.* ----
	spacesUpsert := mcp.NewTool("spaces.upsert",
		mcp.WithDescription("Create or update a space definition with TTL and ACL"),
		mcp.WithString("name", mcp.Required()),
		mcp.WithNumber("ttl_seconds"),
		mcp.WithString("acl_json", mcp.Description("JSON map principal->role (reader|writer|admin)")),
	)
	s.AddTool(spacesUpsert, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name, err := req.RequireString("name")
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("missing name: %v", err)), nil
		}

		ttl := getNumberParam(req, "ttl_seconds")

		acl := map[string]string{}
		aclStr := getStringParam(req, "acl_json")
		if aclStr != "" {
			if err := json.Unmarshal([]byte(aclStr), &acl); err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("invalid acl_json: %v", err)), nil
			}
		}

		m := map[string]memory.SpaceRole{}
		for p, role := range acl {
			m[p] = parseRole(role)
		}
		app.spaces.Upsert(name, time.Duration(int(ttl))*time.Second, m)
		return mcp.NewToolResultText("ok"), nil
	})

	spacesGrant := mcp.NewTool("spaces.grant",
		mcp.WithDescription("Grant a role to a principal for a space"),
		mcp.WithString("name", mcp.Required()),
		mcp.WithString("principal", mcp.Required()),
		mcp.WithString("role", mcp.Required()),
		mcp.WithNumber("ttl_seconds"),
	)
	s.AddTool(spacesGrant, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name, err := req.RequireString("name")
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("missing name: %v", err)), nil
		}
		principal, err := req.RequireString("principal")
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("missing principal: %v", err)), nil
		}
		roleStr, err := req.RequireString("role")
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("missing role: %v", err)), nil
		}

		ttl := int(getNumberParam(req, "ttl_seconds"))
		if ttl <= 0 {
			ttl = 3600
		}

		if err := app.spaces.Grant(name, principal, parseRole(roleStr), time.Duration(ttl)*time.Second); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText("ok"), nil
	})

	spacesRevoke := mcp.NewTool("spaces.revoke",
		mcp.WithDescription("Revoke a principal from a space"),
		mcp.WithString("name", mcp.Required()),
		mcp.WithString("principal", mcp.Required()),
	)
	s.AddTool(spacesRevoke, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		name, err := req.RequireString("name")
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("missing name: %v", err)), nil
		}
		principal, err := req.RequireString("principal")
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("missing principal: %v", err)), nil
		}
		app.spaces.Revoke(name, principal)
		return mcp.NewToolResultText("ok"), nil
	})

	spacesList := mcp.NewTool("spaces.list",
		mcp.WithDescription("List spaces visible to a principal"),
		mcp.WithString("principal", mcp.Required()),
	)
	s.AddTool(spacesList, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		principal, err := req.RequireString("principal")
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("missing principal: %v", err)), nil
		}
		list := app.spaces.List(principal)
		res, _ := mcp.NewToolResultJSON(list)
		return res, nil
	})

	// ---- Shared session tools ----
	sharedJoin := mcp.NewTool("shared.join",
		mcp.WithDescription("Ensure a principal view exists and join a space"),
		mcp.WithString("principal", mcp.Required()),
		mcp.WithString("space", mcp.Required()),
	)
	s.AddTool(sharedJoin, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		p, err := req.RequireString("principal")
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("missing principal: %v", err)), nil
		}
		space, err := req.RequireString("space")
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("missing space: %v", err)), nil
		}
		ss := app.sharedFor(p)
		if err := ss.Join(space); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText("ok"), nil
	})

	sharedLeave := mcp.NewTool("shared.leave",
		mcp.WithDescription("Leave a space in principal view"),
		mcp.WithString("principal", mcp.Required()),
		mcp.WithString("space", mcp.Required()),
	)
	s.AddTool(sharedLeave, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		p, err := req.RequireString("principal")
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("missing principal: %v", err)), nil
		}
		space, err := req.RequireString("space")
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("missing space: %v", err)), nil
		}
		app.sharedFor(p).Leave(space)
		return mcp.NewToolResultText("ok"), nil
	})

	sharedAdd := mcp.NewTool("shared.add_short_to",
		mcp.WithDescription("Add short-term memory directly to a shared space buffer"),
		mcp.WithString("principal", mcp.Required()),
		mcp.WithString("space", mcp.Required()),
		mcp.WithString("content", mcp.Required()),
		mcp.WithString("metadata_json"),
	)
	s.AddTool(sharedAdd, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		p, err := req.RequireString("principal")
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("missing principal: %v", err)), nil
		}
		space, err := req.RequireString("space")
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("missing space: %v", err)), nil
		}
		content, err := req.RequireString("content")
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("missing content: %v", err)), nil
		}

		meta := map[string]string{}
		metaStr := getStringParam(req, "metadata_json")
		if metaStr != "" {
			if err := json.Unmarshal([]byte(metaStr), &meta); err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("invalid metadata_json: %v", err)), nil
			}
		}

		if err := app.sharedFor(p).AddShortTo(space, content, meta); err != nil {
			return mcp.NewToolResultError(err.Error()), nil
		}
		return mcp.NewToolResultText("ok"), nil
	})

	sharedRetrieve := mcp.NewTool("shared.retrieve",
		mcp.WithDescription("Retrieve merged (local+spaces) or only shared if only_shared=true"),
		mcp.WithString("principal", mcp.Required()),
		mcp.WithString("query", mcp.Required()),
		mcp.WithNumber("limit"),
		mcp.WithString("only_shared"),
	)
	s.AddTool(sharedRetrieve, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		p, err := req.RequireString("principal")
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("missing principal: %v", err)), nil
		}
		q, err := req.RequireString("query")
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("missing query: %v", err)), nil
		}

		limit := int(getNumberParam(req, "limit"))
		if limit <= 0 {
			limit = 8
		}

		only := strings.ToLower(getStringParam(req, "only_shared")) == "true"

		var (
			recs []memory.MemoryRecord
			rerr error
		)
		if only {
			recs, rerr = app.sharedFor(p).RetrieveShared(ctx, q, limit)
		} else {
			recs, rerr = app.sharedFor(p).Retrieve(ctx, q, limit)
		}
		if rerr != nil {
			return mcp.NewToolResultError(rerr.Error()), nil
		}
		res, _ := mcp.NewToolResultJSON(recs)
		return res, nil
	})

	// Tool: get_or_create_session
	getOrCreateSession := mcp.NewTool("get_or_create_session",
		mcp.WithDescription("Get existing session ID from disk, or create a new one if none exists"),
	)
	s.AddTool(getOrCreateSession, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		// Try to load existing session
		sessionID, err := loadSessionID()
		if err != nil {
			return mcp.NewToolResultError(fmt.Sprintf("failed to load session: %v", err)), nil
		}

		created := false
		if sessionID == "" {
			// Create new session if none exists
			sessionID = fmt.Sprintf("sess-%d", time.Now().UnixNano())
			if err := saveSessionID(sessionID); err != nil {
				return mcp.NewToolResultError(fmt.Sprintf("failed to save session: %v", err)), nil
			}
			created = true
		}

		res, _ := mcp.NewToolResultJSON(map[string]any{
			"session_id": sessionID,
			"created":    created,
			"loaded":     !created,
		})
		return res, nil
	})

	// Update the initialize tool to save the session ID:
	// Replace your existing initTool with:

	metrics := mcp.NewTool("engine.metrics", mcp.WithDescription("Return engine metrics snapshot")) // This was already correct, but including for completeness
	s.AddTool(metrics, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		res, _ := mcp.NewToolResultJSON(app.engine.MetricsSnapshot())
		return res, nil
	})

	// ---- start transport ----
	switch strings.ToLower(*transport) {
	case "stdio":
		if err := server.ServeStdio(s); err != nil {
			log.Fatal(err)
		}
	case "http":
		log.SetOutput(os.Stderr)
		log.Printf("[MCP] Starting HTTP server on %s", *addr)

		h := server.NewStreamableHTTPServer(s)

		if err := h.Start(*addr); err != nil {
			log.Fatalf("failed to start MCP HTTP server: %v", err)
		}

	default:
		log.Fatal("unknown transport: ", *transport)
	}
}

func envOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func envIntOrDefault(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return def
}

func mustEnv(k string) string {
	v := os.Getenv(k)
	if v == "" {
		panic(fmt.Errorf("missing env %s", k))
	}
	return v
}

func atoi(s string, def int) int {
	i, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return i
}

func parseRole(s string) memory.SpaceRole {
	s = strings.ToLower(strings.TrimSpace(s))
	switch s {
	case "admin":
		return memory.SpaceRoleAdmin
	case "writer", "write":
		return memory.SpaceRoleWriter
	default:
		return memory.SpaceRoleReader
	}
}

func stringMapToJSON(m map[string]string) string {
	b, _ := json.Marshal(m)
	return string(b)
}

func isBinaryContent(data []byte) bool {
	// Check first 8192 bytes for null bytes (binary indicator)
	checkLen := 8192
	if len(data) < checkLen {
		checkLen = len(data)
	}

	for i := 0; i < checkLen; i++ {
		if data[i] == 0 {
			return true
		}
	}
	return false
}

// Add these functions to your main.go file

// getSessionDir returns the path to ~/.memory-bank-mcp directory
func getSessionDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("failed to get home directory: %w", err)
	}
	return filepath.Join(home, ".memory-bank-mcp"), nil
}

// ensureSessionDir creates the session directory if it doesn't exist
func ensureSessionDir() (string, error) {
	dir, err := getSessionDir()
	if err != nil {
		return "", err
	}

	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("failed to create session directory: %w", err)
	}

	return dir, nil
}

// saveSessionID persists the session ID to disk
func saveSessionID(sessionID string) error {
	dir, err := ensureSessionDir()
	if err != nil {
		return err
	}

	filePath := filepath.Join(dir, "session_id")
	if err := os.WriteFile(filePath, []byte(sessionID), 0644); err != nil {
		return fmt.Errorf("failed to write session ID: %w", err)
	}

	log.Printf("Saved session ID to %s", filePath)
	return nil
}

// loadSessionID retrieves the session ID from disk, returns empty string if not found
func loadSessionID() (string, error) {
	dir, err := getSessionDir()
	if err != nil {
		return "", err
	}

	filePath := filepath.Join(dir, "session_id")
	data, err := os.ReadFile(filePath)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil // Not an error, just no saved session
		}
		return "", fmt.Errorf("failed to read session ID: %w", err)
	}

	sessionID := strings.TrimSpace(string(data))
	log.Printf("Loaded session ID from %s", filePath)
	return sessionID, nil
}
