# Hackless MCP Platform

Go MCP server for the Hackless public API.

This is the public distribution repo for the Hackless MCP server. It is meant to be installed with `go install` and used locally by Claude, while the server itself talks to the production Hackless API.

## Environment

- `HACKLESS_BASE_URL` - default `https://hackless.dev`
- `HACKLESS_COOKIE` - optional session cookie for authenticated tools
- `ADDR` - listen address, default `:8000`

## Run locally

```bash
cd hackless-mcp-platform
go run .
```

For manual HTTP smoke tests only:

```bash
go run . --http
```

## Use with Claude

Build the binary first:

```bash
cd hackless-mcp-platform
go build -o hackless-mcp
```

Then register it in Claude as a local stdio MCP server:

```bash
claude mcp add hackless -- /absolute/path/to/hackless-mcp-platform/hackless-mcp
```

To point it at production explicitly, pass the base URL:

```bash
HACKLESS_BASE_URL=https://hackless.dev /absolute/path/to/hackless-mcp-platform/hackless-mcp
```

If the server needs to read your logged-in Hackless session, pass the cookie as an environment variable when you launch it:

```bash
HACKLESS_COOKIE='your-session-cookie' /absolute/path/to/hackless-mcp-platform/hackless-mcp
```

For API-key auth, set:

```bash
HACKLESS_API_KEY='your-mcp-key' /absolute/path/to/hackless-mcp-platform/hackless-mcp
```

Install via Go:

```bash
go install github.com/Rarex224/hackless-mcp-platform@latest
```

## Railway

If you want to host the MCP server as a separate service, deploy this repo as its own Railway service and set:

- `HACKLESS_BASE_URL=https://hackless.dev`
- `HACKLESS_API_KEY=<your_hackless_api_key>`

For Claude Desktop usage you usually do not need Railway; the normal flow is to install the binary locally and let Claude launch it as a stdio MCP server.

## Tools

- `list_challenges`
- `get_my_progress`
- `view_leaderboard`
- `submit_flag`

## Notes

This server speaks JSON-RPC over stdio for Claude and also supports HTTP test mode on `--http`.
It defaults to `https://hackless.dev` and can be overridden with `HACKLESS_BASE_URL`, `--base-url`, or the first positional argument.
It uses the Hackless public REST endpoints instead of the internal tRPC router.
