// path: main.go
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	// MCP server + types
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	// Memory engine (wired but currently optional to run)
	mem "github.com/Protocol-Lattice/go-agent/src/memory"
)

// -----------------------------------------------------------------------------
// Very small, safe in-process memory layer
// -----------------------------------------------------------------------------
//
// Notes:
// - This file compiles cleanly against github.com/mark3labs/mcp-go v0.43+
//   and github.com/Protocol-Lattice/go-agent v0.6+.
// - It exposes an MCP server with a few memory-oriented tools.
// - It also wires the Protocol-Lattice memory engine (in-memory store + embedder)
//   so you can switch storage/recall paths to mem.SessionMemory later.
// - The initial storage below is a minimal, concurrency-safe ring buffer per (session, space)
//   to make the server usable immediately. Swap the calls in the handlers where marked
//   with the go-agent memory engine if/when you’re ready.

type Message struct {
	Ts      time.Time         `json:"ts"`
	Role    string            `json:"role"`    // "user" | "agent" | "system"
	Content string            `json:"content"` // raw text
	Meta    map[string]any    `json:"meta,omitempty"`
	Vec     []float32         `json:"vec,omitempty"` // for future embedding use
	Tags    []string          `json:"tags,omitempty"`
	Score   float64           `json:"score,omitempty"`
	Extra   map[string]string `json:"extra,omitempty"`
}

type ring struct {
	data []Message
	next int
	size int
}

func newRing(capacity int) *ring {
	return &ring{data: make([]Message, 0, capacity), next: 0, size: capacity}
}

func (r *ring) append(m Message) {
	if len(r.data) < r.size {
		r.data = append(r.data, m)
		return
	}
	r.data[r.next] = m
	r.next = (r.next + 1) % r.size
}

func (r *ring) sliceNewest(limit int) []Message {
	n := len(r.data)
	if limit <= 0 || limit > n {
		limit = n
	}
	out := make([]Message, 0, limit)
	for i := 0; i < limit; i++ {
		idx := (r.next - 1 - i + n) % n
		out = append(out, r.data[idx])
	}
	return out
}

type bucketKey struct {
	Session string
	Space   string
}

type InProcessMemory struct {
	mu     sync.RWMutex
	store  map[bucketKey]*ring
	limit  int
	engine *mem.Engine // optional: wired, not required to run
}

func NewInProcessMemory(limit int, engine *mem.Engine) *InProcessMemory {
	return &InProcessMemory{
		store:  make(map[bucketKey]*ring),
		limit:  limit,
		engine: engine,
	}
}

func (m *InProcessMemory) Append(ctx context.Context, session, space, role, content string, meta map[string]any) (Message, error) {
	if strings.TrimSpace(session) == "" || strings.TrimSpace(space) == "" {
		return Message{}, errors.New("session and space are required")
	}
	msg := Message{
		Ts:      time.Now().UTC(),
		Role:    role,
		Content: content,
		Meta:    meta,
	}
	// (Optional) If you want to embed now via go-agent engine:
	// if m.engine != nil {
	//     vec, err := m.engine.Embed(ctx, content)
	//     if err == nil {
	//         msg.Vec = vec
	//     }
	// }

	m.mu.Lock()
	defer m.mu.Unlock()
	key := bucketKey{Session: session, Space: space}
	r, ok := m.store[key]
	if !ok {
		r = newRing(m.limit)
		m.store[key] = r
	}
	r.append(msg)
	return msg, nil
}

func (m *InProcessMemory) Recent(_ context.Context, session, space string, limit int) ([]Message, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	key := bucketKey{Session: session, Space: space}
	r, ok := m.store[key]
	if !ok {
		return nil, nil
	}
	return r.sliceNewest(limit), nil
}

func (m *InProcessMemory) Search(_ context.Context, session, space, query string, limit int) ([]Message, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	key := bucketKey{Session: session, Space: space}
	r, ok := m.store[key]
	if !ok {
		return nil, nil
	}
	// naive substring search (replace with engine retrieval when ready)
	matches := make([]Message, 0, limit)
	for i := len(r.data) - 1; i >= 0 && len(matches) < limit; i-- {
		if strings.Contains(strings.ToLower(r.data[i].Content), strings.ToLower(query)) {
			matches = append(matches, r.data[i])
		}
	}
	return matches, nil
}

// -----------------------------------------------------------------------------
// Wire go-agent memory engine (optional but ready)
// -----------------------------------------------------------------------------

type EngineBundle struct {
	Store   mem.VectorStore
	Bank    *mem.MemoryBank
	Engine  *mem.Engine
	Session *mem.SessionMemory
}

func buildEngineBundle(ctx context.Context) (*EngineBundle, error) {
	// Default: in-memory vector store
	store := mem.NewInMemoryStore()

	// Engine options + embedder (replace AutoEmbedder with your provider if desired)
	opts := mem.DefaultOptions()
	engine := mem.NewEngine(store, opts).WithEmbedder(mem.AutoEmbedder())

	// Session memory with a memory bank backing it
	bank := mem.NewMemoryBankWithStore(store)
	session := mem.NewSessionMemory(bank, 32).WithEngine(engine)

	return &EngineBundle{
		Store:   store,
		Bank:    bank,
		Engine:  engine,
		Session: session,
	}, nil
}

// -----------------------------------------------------------------------------
// MCP server setup and tool handlers
// -----------------------------------------------------------------------------

func main() {
	ctx := context.Background()

	// Build go-agent memory engine (safe to keep even if unused at first)
	var engineBundle *EngineBundle
	{
		eb, err := buildEngineBundle(ctx)
		if err != nil {
			log.Printf("⚠️ memory engine init failed (continuing with minimal store): %v", err)
		}
		engineBundle = eb
	}

	// Minimal in-process memory (ring buffer per session+space)
	inproc := NewInProcessMemory(512, nil)
	if engineBundle != nil {
		inproc.engine = engineBundle.Engine
	}

	srv := server.NewMCPServer("memory-bank-mcp", "0.1.0", nil)

	// Tool: memory.store
	// Stores a message in (session, space) with role + content + optional metadata.
	storeTool := mcp.NewTool(
		"memory.store",
		mcp.WithDescription("Store a memory message into a session/space."),
		mcp.WithString("session", mcp.Required(), mcp.Description("Session identifier")),
		mcp.WithString("space", mcp.Required(), mcp.Description("Space/channel name (e.g., 'default', 'team:core')")),
		mcp.WithString("role", mcp.Description("Role of the author: user|agent|system"), mcp.DefaultString("user")),
		mcp.WithString("content", mcp.Required(), mcp.Description("Text content to store")),
		mcp.WithObject("meta", mcp.Description("Optional metadata object")),
	)

	srv.AddTool(storeTool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		session, err := req.RequireString("session")
		if err != nil {
			return nil, err
		}
		space, err := req.RequireString("space")
		if err != nil {
			return nil, err
		}
		role, err := req.RequireString("role")
		if err != nil || role == "" {
			role = "user"
		}
		content, err := req.RequireString("content")
		if err != nil {
			return nil, err
		}

		var meta map[string]any
		// meta is optional
		if raw := req.Params.Arguments.(map[string]string)["meta"]; raw != "" {
			// Make sure it's JSON-objecty
			b, _ := json.Marshal(raw)
			_ = json.Unmarshal(b, &meta)
			if meta == nil {
				meta = map[string]any{}
			}
		}

		msg, err := inproc.Append(ctx, session, space, role, content, meta)
		if err != nil {
			return nil, err
		}

		// Return a small payload
		out := map[string]any{
			"stored":  true,
			"session": session,
			"space":   space,
			"role":    role,
			"ts":      msg.Ts,
		}
		res, err := mcp.NewToolResultJSON(out)
		if err != nil {
			return nil, err
		}
		return res, nil
	})

	// Tool: memory.recent
	// Returns last N messages for (session, space).
	recentTool := mcp.NewTool(
		"memory.recent",
		mcp.WithDescription("Get most recent messages from a session/space."),
		mcp.WithString("session", mcp.Required(), mcp.Description("Session identifier")),
		mcp.WithString("space", mcp.Required(), mcp.Description("Space/channel name")),
		mcp.WithNumber("limit", mcp.Description("Max messages to return"), mcp.DefaultNumber(20)),
	)

	srv.AddTool(recentTool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		session, err := req.RequireString("session")
		if err != nil {
			return nil, err
		}
		space, err := req.RequireString("space")
		if err != nil {
			return nil, err
		}
		limit, err := req.RequireInt("limit")
		if err != nil || limit <= 0 {
			limit = 20
		}

		items, err := inproc.Recent(ctx, session, space, limit)
		if err != nil {
			return nil, err
		}

		res, err := mcp.NewToolResultJSON(map[string]any{
			"session":  session,
			"space":    space,
			"messages": items,
			"count":    len(items),
		})
		if err != nil {
			return nil, err
		}
		return res, nil
	})

	// Tool: memory.search
	// Naive substring search over stored content. Replace with go-agent retrieval later.
	searchTool := mcp.NewTool(
		"memory.search",
		mcp.WithDescription("Search messages in a session/space by substring (upgradeable to vector search)."),
		mcp.WithString("session", mcp.Required(), mcp.Description("Session identifier")),
		mcp.WithString("space", mcp.Required(), mcp.Description("Space/channel name")),
		mcp.WithString("query", mcp.Required(), mcp.Description("Query string to match")),
		mcp.WithNumber("limit", mcp.Description("Max results"), mcp.DefaultNumber(10)),
	)

	srv.AddTool(searchTool, func(ctx context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		session, err := req.RequireString("session")
		if err != nil {
			return nil, err
		}
		space, err := req.RequireString("space")
		if err != nil {
			return nil, err
		}
		query, err := req.RequireString("query")
		if err != nil {
			return nil, err
		}
		limit, err := req.RequireInt("limit")
		if err != nil || limit <= 0 {
			limit = 10
		}

		items, err := inproc.Search(ctx, session, space, query, limit)
		if err != nil {
			return nil, err
		}

		res, err := mcp.NewToolResultJSON(map[string]any{
			"session": session,
			"space":   space,
			"query":   query,
			"results": items,
			"count":   len(items),
		})
		if err != nil {
			return nil, err
		}
		return res, nil
	})

	// Tool: memory.health
	healthTool := mcp.NewTool(
		"memory.health",
		mcp.WithDescription("Health check / info about the memory MCP server."),
	)

	srv.AddTool(healthTool, func(ctx context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		engineReady := engineBundle != nil && engineBundle.Engine != nil
		payload := map[string]any{
			"name":          "memory-bank-mcp",
			"version":       "0.1.0",
			"engine_wired":  engineReady,
			"store_backend": "inproc-ring",
			"time_utc":      time.Now().UTC().Format(time.RFC3339),
		}
		res, err := mcp.NewToolResultJSON(payload)
		if err != nil {
			return nil, err
		}
		return res, nil
	})

	// Serve over stdio by default (MCP runtime convention)
	if err := server.ServeStdio(srv /* no stdio opts */); err != nil {
		// If you're testing locally outside of a client, you can run a quick “inline call”
		// by setting MCP_TRANSPORT=none to prevent stdio loop.
		if os.Getenv("MCP_TRANSPORT") == "none" {
			fmt.Println("Server initialized (noop mode).")
			return
		}
		log.Fatalf("stdio server error: %v", err)
	}
}
