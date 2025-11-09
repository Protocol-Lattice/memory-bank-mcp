# Memory Bank MCP Server

A production-ready **Model Context Protocol (MCP)** server that provides a powerful, vector-native memory bank for AI agents. Built with the [Protocol-Lattice Go Agent Framework](https://github.com/Protocol-Lattice/go-agent), this server offers persistent, searchable, and shareable memory with multiple database backends.

- **Server Name**: `memory-bank-mcp`
- **Version**: `0.1.0`

## Features

- **Multiple Vector Store Backends**: Persist memories in your preferred database.
  - **In-Memory**: For quick tests and ephemeral storage.
  - **PostgreSQL**: Using the `pgvector` extension.
  - **Qdrant**: A dedicated vector database.
  - **MongoDB**: Using Atlas Vector Search.

- **Rich Memory Management**:
  - **Short-Term Buffer**: Temporarily store memories for a session before committing them to long-term storage.
  - **Long-Term Persistence**: Embed and store memories for semantic retrieval across sessions.
  - **Contextual Retrieval**: Fetch relevant memories based on a query, combining both short-term and long-term results.

- **Shared Memory with "Spaces"**:
  - Create shared memory "spaces" where multiple agents or users can collaborate.
  - Fine-grained access control (ACLs) with `reader`, `writer`, and `admin` roles.
  - Time-to-live (TTL) support for grants and spaces.

- **Dynamic Embeddings**: Uses `AutoEmbedder` from the Go Agent Framework, allowing you to configure the embedding model via environment variables (e.g., OpenAI, Gemini, local models).

## Configuration

Configure the server using environment variables.

### Memory Store

Set `MEMORY_STORE` to one of `inmemory`, `postgres`, `qdrant`, or `mongo`.

- **PostgreSQL**:
  ```bash
  export MEMORY_STORE="postgres"
  export POSTGRES_DSN="postgres://user:pass@host:port/db?sslmode=disable"
  ```
- **Qdrant**:
  ```bash
  export MEMORY_STORE="qdrant"
  export QDRANT_URL="http://localhost:6333"
  export QDRANT_COLLECTION="memories"
  export QDRANT_API_KEY="..." # Optional
  ```
- **MongoDB**:
  ```bash
  export MEMORY_STORE="mongo"
  export MONGO_URI="mongodb+srv://..."
  export MONGO_DATABASE="main"
  export MONGO_COLLECTION="memories"
  ```

### Other Settings

- `SHORT_TERM_SIZE`: Max items in the short-term buffer per session. (Default: `20`)
- `DEFAULT_SPACE_TTL_SEC`: Default TTL for spaces in seconds. (Default: `86400` / 24 hours)

### Embedding Model

The server uses `AutoEmbedder`, which respects `ADK_EMBED_*` environment variables from the Go Agent Framework. For example, to use Gemini:

```bash
export ADK_EMBED_PROVIDER="gemini"
export ADK_EMBED_MODEL="text-embedding-004"
export GEMINI_API_KEY="YOUR_GEMINI_API_KEY"
```

## Quick Start

```bash
go install github.com/Protocol-Lattice/memory-bank-mcp@latest
# Run the server (defaults to stdio transport)
memory-bank-mcp
```

## Available MCP Tools

The server exposes a comprehensive set of tools for memory manipulation.

### Core Memory
- `health.ping`: Check if the server is running.
- `memory.embed`: Get the vector embedding for a piece of text.
- `memory.add_short`: Add a memory to a session's short-term buffer.
- `memory.flush`: Persist a session's short-term buffer to the long-term vector store.
- `memory.store_long`: Directly embed and store a memory in the long-term store.
- `memory.retrieve_context`: Retrieve relevant memories for a query from a session.

### Spaces (Shared Memory)
- `spaces.upsert`: Create or update a shared space with a TTL and ACL.
- `spaces.grant`: Grant a role (`reader`, `writer`, `admin`) to a principal for a space.
- `spaces.revoke`: Revoke a principal's access to a space.
- `spaces.list`: List all spaces a principal has access to.

### Shared Sessions
- `shared.join`: Make a principal's session view include a specific space.
- `shared.leave`: Remove a space from a principal's session view.
- `shared.add_short_to`: Add a short-term memory directly to a shared space.
- `shared.retrieve`: Retrieve memories from a principal's merged view (local + joined spaces).

## MCP Configuration:
```
{
	"servers": {
    "memory-bank": {
      "command": "/usr/local/bin/memory-bank-mcp",
      "args": ["-transport", "http", "-addr", ":8080"]
    }
	},
	"inputs": []
}
```
### Diagnostics
- `engine.metrics`: Get a snapshot of the memory engine's performance metrics.
