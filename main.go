package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"
)

type jsonRPCRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type jsonRPCResponse struct {
	JSONRPC string    `json:"jsonrpc"`
	ID      any       `json:"id,omitempty"`
	Result  any       `json:"result,omitempty"`
	Error   *rpcError `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type toolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"inputSchema"`
}

type mcpServer struct {
	baseURL      string
	cookie       string
	apiKey       string
	eventApiKey  string
	client       *http.Client
}

// Global tools always available (require user API key)
var globalTools = []toolDef{
	{
		Name:        "list_challenges",
		Description: "List public Hackless challenges.",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
	},
	{
		Name:        "get_my_progress",
		Description: "Get the authenticated user's profile and progress (requires API key).",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
	},
	{
		Name:        "view_leaderboard",
		Description: "Get the public leaderboard.",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
	},
	{
		Name:        "submit_flag",
		Description: "Submit a flag for a challenge.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"slug": map[string]any{"type": "string", "description": "Challenge slug"},
				"flag": map[string]any{"type": "string", "description": "Flag to submit"},
			},
			"required": []string{"slug", "flag"},
		},
	},
}

// Event tools keyed by their enabled flag (require event API key)
var eventToolDefs = map[string]toolDef{
	"fetchEventDetails": {
		Name:        "get_event",
		Description: "Get comprehensive information about the current event.",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
	},
	"updateEvent": {
		Name:        "update_event",
		Description: "Update event details like title, schedule, scope or location.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"title":    map[string]any{"type": "string"},
				"location": map[string]any{"type": "string"},
				"scope":    map[string]any{"type": "string", "enum": []string{"public", "community", "private"}},
			},
		},
	},
	"listWidgets": {
		Name:        "get_widgets",
		Description: "List all challenges/widgets for the current event.",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
	},
	"getWidget": {
		Name:        "get_single_widget",
		Description: "Get details of a specific challenge/widget by slug.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"slug": map[string]any{"type": "string", "description": "Challenge slug"},
			},
			"required": []string{"slug"},
		},
	},
	"createWidget": {
		Name:        "create_widget",
		Description: "Add a new challenge/widget to the event.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"title":       map[string]any{"type": "string"},
				"slug":        map[string]any{"type": "string"},
				"difficulty":  map[string]any{"type": "string", "enum": []string{"Easy", "Medium", "Hard", "Insane"}},
				"points":      map[string]any{"type": "integer"},
				"type":        map[string]any{"type": "string", "enum": []string{"web", "pwn", "reverse", "mobile", "file"}},
				"rewardBadge": map[string]any{"type": "string"},
			},
			"required": []string{"title", "slug", "difficulty", "points", "type", "rewardBadge"},
		},
	},
	"updateWidget": {
		Name:        "update_widget",
		Description: "Update a challenge/widget in the event.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"slug":       map[string]any{"type": "string", "description": "Widget slug to update"},
				"title":      map[string]any{"type": "string"},
				"difficulty": map[string]any{"type": "string"},
				"points":     map[string]any{"type": "integer"},
			},
			"required": []string{"slug"},
		},
	},
	"deleteWidget": {
		Name:        "delete_widget",
		Description: "Remove a challenge/widget from the event.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"slug": map[string]any{"type": "string", "description": "Widget slug to delete"},
			},
			"required": []string{"slug"},
		},
	},
}

// Internal event tool name -> enabled key mapping for reverse lookup
var toolNameToKey = map[string]string{}

func init() {
	for key, def := range eventToolDefs {
		toolNameToKey[def.Name] = key
	}
}

func main() {
	httpMode := flag.Bool("http", false, "run as a local HTTP test server instead of stdio MCP")
	addr := flag.String("addr", ":8000", "HTTP listen address")
	baseURLFlag := flag.String("base-url", "", "Hackless API base URL")
	flag.Parse()

	baseURL := resolveBaseURL(*baseURLFlag, flag.Args())
	srv := &mcpServer{
		baseURL:     baseURL,
		cookie:      env("HACKLESS_COOKIE", ""),
		apiKey:      env("HACKLESS_API_KEY", ""),
		eventApiKey: env("HACKLESS_EVENT_API_KEY", ""),
		client: &http.Client{
			Timeout: 20 * time.Second,
		},
	}

	// If HACKLESS_API_KEY looks like an event key (starts with hk_), treat it as event key too
	if srv.eventApiKey == "" && strings.HasPrefix(srv.apiKey, "hk_") {
		srv.eventApiKey = srv.apiKey
	}

	if *httpMode {
		runHTTP(srv, *addr)
		return
	}

	runStdio(srv)
}

func runHTTP(srv *mcpServer, addr string) {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"ok": "true"})
	})
	mux.HandleFunc("/mcp", srv.handleHTTPMCP)

	log.Printf("Hackless MCP HTTP test server listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, mux))
}

func runStdio(srv *mcpServer) {
	log.Println("Hackless MCP stdio server started")

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 1024), 1024*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}

		var req jsonRPCRequest
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			writeStdioResponse(jsonRPCResponse{
				JSONRPC: "2.0",
				Error: &rpcError{
					Code:    -32700,
					Message: "invalid JSON-RPC request",
					Data:    err.Error(),
				},
			})
			continue
		}

		if req.ID == nil {
			continue
		}

		resp := srv.handleJSONRPC(req)
		writeStdioResponse(resp)
	}

	if err := scanner.Err(); err != nil {
		log.Printf("stdio read error: %v", err)
	}
}

func writeStdioResponse(resp jsonRPCResponse) {
	enc, err := json.Marshal(resp)
	if err != nil {
		log.Printf("stdio encode error: %v", err)
		return
	}
	fmt.Println(string(enc))
}

func (s *mcpServer) handleHTTPMCP(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"message": "Hackless MCP endpoint. Use POST /mcp with JSON-RPC.",
		})
	case http.MethodPost:
		var req jsonRPCRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(jsonRPCResponse{
				JSONRPC: "2.0",
				Error: &rpcError{
					Code:    -32700,
					Message: "invalid JSON-RPC request",
					Data:    err.Error(),
				},
			})
			return
		}
		resp := s.handleJSONRPC(req)
		w.Header().Set("Content-Type", "application/json")
		if resp.Error != nil {
			w.WriteHeader(http.StatusBadRequest)
		}
		_ = json.NewEncoder(w).Encode(resp)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *mcpServer) handleJSONRPC(req jsonRPCRequest) jsonRPCResponse {
	switch req.Method {
	case "initialize":
		return jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"protocolVersion": "2024-11-05",
				"serverInfo": map[string]any{
					"name":    "hackless-mcp",
					"version": "0.2.0",
				},
				"capabilities": map[string]any{
					"tools": map[string]any{"listChanged": false},
				},
			},
		}
	case "tools/list":
		tools := s.buildToolList()
		return jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"tools": tools,
			},
		}
	case "tools/call":
		payload, err := s.callTool(req.Params)
		if err != nil {
			return jsonRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error: &rpcError{
					Code:    -32000,
					Message: err.Error(),
				},
			}
		}
		return jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  payload,
		}
	default:
		return jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &rpcError{
				Code:    -32601,
				Message: "method not found",
				Data:    req.Method,
			},
		}
	}
}

// buildToolList returns the list of available tools based on API keys and event configuration.
func (s *mcpServer) buildToolList() []toolDef {
	tools := make([]toolDef, len(globalTools))
	copy(tools, globalTools)

	if s.eventApiKey == "" {
		return tools
	}

	// Fetch event to get enabled tools
	var eventResp struct {
		Event struct {
			Tools []struct {
				Key     string `json:"key"`
				Enabled bool   `json:"enabled"`
			} `json:"tools"`
		} `json:"event"`
	}
	if err := s.getJSONWithEventKey("/api/public/events/current", &eventResp); err != nil {
		log.Printf("failed to fetch event tools: %v", err)
		return tools
	}

	for _, t := range eventResp.Event.Tools {
		if t.Enabled {
			if def, ok := eventToolDefs[t.Key]; ok {
				tools = append(tools, def)
			}
		}
	}

	return tools
}

func (s *mcpServer) callTool(params json.RawMessage) (any, error) {
	var payload struct {
		Name      string         `json:"name"`
		Arguments map[string]any `json:"arguments"`
	}
	if err := json.Unmarshal(params, &payload); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}

	switch payload.Name {
	// Global tools
	case "list_challenges":
		var data any
		if err := s.getJSON("/api/public/challenges", &data); err != nil {
			return nil, err
		}
		return mcpText(data), nil

	case "get_my_progress":
		var data any
		if err := s.getJSON("/api/public/me", &data); err != nil {
			return nil, err
		}
		return mcpText(data), nil

	case "view_leaderboard":
		var data any
		if err := s.getJSON("/api/public/leaderboard", &data); err != nil {
			return nil, err
		}
		return mcpText(data), nil

	case "submit_flag":
		slug, _ := payload.Arguments["slug"].(string)
		flag, _ := payload.Arguments["flag"].(string)
		if slug == "" || flag == "" {
			return nil, fmt.Errorf("slug and flag are required")
		}
		var data any
		if err := s.postJSON(fmt.Sprintf("/api/public/challenges/%s/submit", slug), map[string]string{"flag": flag}, &data); err != nil {
			return nil, err
		}
		return mcpText(data), nil

	// Event tools (require event API key)
	case "get_event":
		if s.eventApiKey == "" {
			return nil, fmt.Errorf("event API key required for this tool")
		}
		var data any
		if err := s.getJSONWithEventKey("/api/public/events/current", &data); err != nil {
			return nil, err
		}
		return mcpText(data), nil

	case "update_event":
		if s.eventApiKey == "" {
			return nil, fmt.Errorf("event API key required for this tool")
		}
		// Get event slug first
		var eventResp struct {
			Event struct {
				Slug string `json:"slug"`
			} `json:"event"`
		}
		if err := s.getJSONWithEventKey("/api/public/events/current", &eventResp); err != nil {
			return nil, err
		}
		var data any
		if err := s.patchJSONWithEventKey(fmt.Sprintf("/api/public/events/%s", eventResp.Event.Slug), payload.Arguments, &data); err != nil {
			return nil, err
		}
		return mcpText(data), nil

	case "get_widgets":
		if s.eventApiKey == "" {
			return nil, fmt.Errorf("event API key required for this tool")
		}
		var eventResp struct {
			Event struct {
				Slug       string `json:"slug"`
				Challenges []any  `json:"challenges"`
			} `json:"event"`
		}
		if err := s.getJSONWithEventKey("/api/public/events/current", &eventResp); err != nil {
			return nil, err
		}
		return mcpText(map[string]any{"challenges": eventResp.Event.Challenges}), nil

	case "get_single_widget":
		if s.eventApiKey == "" {
			return nil, fmt.Errorf("event API key required for this tool")
		}
		slug, _ := payload.Arguments["slug"].(string)
		if slug == "" {
			return nil, fmt.Errorf("slug is required")
		}
		var eventResp struct {
			Event struct {
				Challenges []map[string]any `json:"challenges"`
			} `json:"event"`
		}
		if err := s.getJSONWithEventKey("/api/public/events/current", &eventResp); err != nil {
			return nil, err
		}
		for _, ch := range eventResp.Event.Challenges {
			if chSlug, _ := ch["slug"].(string); chSlug == slug {
				return mcpText(ch), nil
			}
		}
		return nil, fmt.Errorf("widget with slug %q not found", slug)

	case "create_widget":
		if s.eventApiKey == "" {
			return nil, fmt.Errorf("event API key required for this tool")
		}
		var eventResp struct {
			Event struct{ Slug string `json:"slug"` } `json:"event"`
		}
		if err := s.getJSONWithEventKey("/api/public/events/current", &eventResp); err != nil {
			return nil, err
		}
		var data any
		if err := s.postJSONWithEventKey(fmt.Sprintf("/api/public/events/%s/challenges", eventResp.Event.Slug), payload.Arguments, &data); err != nil {
			return nil, err
		}
		return mcpText(data), nil

	case "update_widget":
		if s.eventApiKey == "" {
			return nil, fmt.Errorf("event API key required for this tool")
		}
		slug, _ := payload.Arguments["slug"].(string)
		if slug == "" {
			return nil, fmt.Errorf("slug is required")
		}
		var eventResp struct {
			Event struct{ Slug string `json:"slug"` } `json:"event"`
		}
		if err := s.getJSONWithEventKey("/api/public/events/current", &eventResp); err != nil {
			return nil, err
		}
		patch := make(map[string]any)
		for k, v := range payload.Arguments {
			if k != "slug" {
				patch[k] = v
			}
		}
		var data any
		if err := s.patchJSONWithEventKey(fmt.Sprintf("/api/public/events/%s/challenges/%s", eventResp.Event.Slug, slug), patch, &data); err != nil {
			return nil, err
		}
		return mcpText(data), nil

	case "delete_widget":
		if s.eventApiKey == "" {
			return nil, fmt.Errorf("event API key required for this tool")
		}
		slug, _ := payload.Arguments["slug"].(string)
		if slug == "" {
			return nil, fmt.Errorf("slug is required")
		}
		var eventResp struct {
			Event struct{ Slug string `json:"slug"` } `json:"event"`
		}
		if err := s.getJSONWithEventKey("/api/public/events/current", &eventResp); err != nil {
			return nil, err
		}
		var data any
		if err := s.deleteJSONWithEventKey(fmt.Sprintf("/api/public/events/%s/challenges/%s", eventResp.Event.Slug, slug), &data); err != nil {
			return nil, err
		}
		return mcpText(data), nil

	default:
		return nil, fmt.Errorf("unknown tool: %s", payload.Name)
	}
}

func (s *mcpServer) getJSON(path string, out any) error {
	return s.requestJSON(http.MethodGet, path, nil, out, false)
}

func (s *mcpServer) postJSON(path string, body any, out any) error {
	return s.requestJSON(http.MethodPost, path, body, out, false)
}

func (s *mcpServer) getJSONWithEventKey(path string, out any) error {
	return s.requestJSON(http.MethodGet, path, nil, out, true)
}

func (s *mcpServer) postJSONWithEventKey(path string, body any, out any) error {
	return s.requestJSON(http.MethodPost, path, body, out, true)
}

func (s *mcpServer) patchJSONWithEventKey(path string, body any, out any) error {
	return s.requestJSON(http.MethodPatch, path, body, out, true)
}

func (s *mcpServer) deleteJSONWithEventKey(path string, out any) error {
	return s.requestJSON(http.MethodDelete, path, nil, out, true)
}

func (s *mcpServer) requestJSON(method, path string, body any, out any, useEventKey bool) error {
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(payload)
	}

	req, err := http.NewRequest(method, s.baseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	if useEventKey && s.eventApiKey != "" {
		req.Header.Set("X-Hackless-Event-API-Key", s.eventApiKey)
	} else if s.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+s.apiKey)
	}

	if s.cookie != "" {
		req.Header.Set("Cookie", s.cookie)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(raw)))
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(raw, out)
}

func mcpText(data any) map[string]any {
	raw, _ := json.MarshalIndent(data, "", "  ")
	return map[string]any{
		"content": []map[string]any{
			{
				"type": "text",
				"text": string(raw),
			},
		},
	}
}

func env(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func resolveBaseURL(flagValue string, args []string) string {
	if value := strings.TrimSpace(flagValue); value != "" {
		return strings.TrimRight(value, "/")
	}
	for _, arg := range args {
		trimmed := strings.TrimSpace(arg)
		if trimmed != "" && !strings.HasPrefix(trimmed, "-") {
			return strings.TrimRight(trimmed, "/")
		}
	}
	if value := strings.TrimSpace(os.Getenv("HACKLESS_BASE_URL")); value != "" {
		return strings.TrimRight(value, "/")
	}
	return "https://hackless.dev"
}
