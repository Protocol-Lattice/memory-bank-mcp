# memory-bank-mcp

Minimal, production-ready **Model Context Protocol (MCP)** server that provides a lightweight memory “bank” with a simple, concurrency-safe in-process store — and an easy upgrade path to Protocol-Lattice’s vector memory engine.

- Server name/version: `memory-bank-mcp` / `0.1.0`. :contentReference[oaicite:0]{index=0}  
- Ships four tools: `memory.store`, `memory.recent`, `memory.search`, `memory.health`. :contentReference[oaicite:1]{index=1} :contentReference[oaicite:2]{index=2} :contentReference[oaicite:3]{index=3} :contentReference[oaicite:4]{index=4}

---

## Why this exists

- **Drop-in MCP server**: clean stdio server via `mark3labs/mcp-go`. :contentReference[oaicite:5]{index=5}  
- **Works instantly**: uses an internal ring buffer per `(session, space)` so you can store/retrieve right away. :contentReference[oaicite:6]{index=6} :contentReference[oaicite:7]{index=7}  
- **Upgradable**: comes pre-wired to Protocol-Lattice’s memory engine (in-memory vector store + embedder) — you can flip to vector search/session memory later. :contentReference[oaicite:8]{index=8} :contentReference[oaicite:9]{index=9}

---

## Features

- **In-process memory**: ring buffer with default capacity of **512** messages per `(session, space)`. :contentReference[oaicite:10]{index=10}  
- **Thread-safe** appends and reads. :contentReference[oaicite:11]{index=11} :contentReference[oaicite:12]{index=12}  
- **Naive search** (substring) out-of-the-box; replace with vector retrieval when ready. :contentReference[oaicite:13]{index=13}  
- **Health endpoint** with server metadata and engine wiring status. :contentReference[oaicite:14]{index=14}

---

## Requirements

- Go (recent stable).  
- `github.com/mark3labs/mcp-go` (tested on `v0.43+`).  
- `github.com/Protocol-Lattice/go-agent/src/memory` (engine wiring; `v0.6+`). :contentReference[oaicite:15]{index=15}

---

## Quick start

```bash
# clone
git clone https://github.com/Protocol-Lattice/memory-bank-mcp
cd memory-bank-mcp

# tidy & build
go mod tidy
go build -o memory-bank-mcp

# run (stdio mode; MCP client will manage the process)
./memory-bank-mcp
