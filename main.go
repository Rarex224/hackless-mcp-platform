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
	baseURL     string
	cookie      string
	apiKey      string
	eventApiKey string
	client      *http.Client
}

// Global tools always available (require user API key)
var globalTools = []toolDef{
	{
		Name:        "health",
		Description: "Check if the Hackless API is reachable.",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
	},
	{
		Name:        "list_challenges",
		Description: "List public Hackless challenges.",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
	},
	{
		Name:        "get_challenge",
		Description: "Get details for a challenge by slug.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"slug": map[string]any{"type": "string", "description": "Challenge slug"},
			},
			"required": []string{"slug"},
		},
	},
	{
		Name:        "get_my_progress",
		Description: "Get the authenticated user's profile and progress (requires API key).",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
	},
	{
		Name:        "get_public_profile",
		Description: "Get a public profile by user ID.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"userId": map[string]any{"type": "string", "description": "User ID"},
			},
			"required": []string{"userId"},
		},
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
	{
		Name:        "list_writeups_for_challenge",
		Description: "List writeups for a challenge that has already been solved.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"slug": map[string]any{"type": "string", "description": "Challenge slug"},
			},
			"required": []string{"slug"},
		},
	},
}

// Event tools require HACKLESS_EVENT_API_KEY.
var eventTools = []toolDef{
	{
		Name:        "get_event",
		Description: "Get the current event details for the configured event API key.",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
	},
	{
		Name:        "update_event",
		Description: "Update the current event title and/or description.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"title":       map[string]any{"type": "string", "description": "New event title"},
				"description": map[string]any{"type": "string", "description": "New event description"},
			},
		},
	},
	{
		Name:        "add_event_challenges",
		Description: "Add challenges to the current event.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"challengeIds": map[string]any{
					"type":        "array",
					"description": "Challenge IDs to add",
					"items":       map[string]any{"type": "string"},
				},
			},
			"required": []string{"challengeIds"},
		},
	},
	{
		Name:        "remove_event_challenge",
		Description: "Remove a challenge from the current event.",
		InputSchema: map[string]any{
			"type": "object",
			"properties": map[string]any{
				"challengeId": map[string]any{"type": "string", "description": "Challenge ID to remove"},
			},
			"required": []string{"challengeId"},
		},
	},
	{
		Name:        "list_event_participants",
		Description: "List participants and scores for the current event.",
		InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
	},
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

// buildToolList returns the list of available tools.
func (s *mcpServer) buildToolList() []toolDef {
	tools := make([]toolDef, 0, len(globalTools)+len(eventTools))
	tools = append(tools, globalTools...)
	tools = append(tools, eventTools...)
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

	case "get_challenge":
		slug, _ := payload.Arguments["slug"].(string)
		if slug == "" {
			return nil, fmt.Errorf("slug is required")
		}
		var data any
		if err := s.getJSON(fmt.Sprintf("/api/public/challenges/%s", slug), &data); err != nil {
			return nil, err
		}
		return mcpText(data), nil

	case "health":
		var data any
		if err := s.getJSON("/api/public/health", &data); err != nil {
			return nil, err
		}
		return mcpText(data), nil

	case "get_my_progress":
		var data any
		if err := s.getJSON("/api/public/me", &data); err != nil {
			return nil, err
		}
		return mcpText(data), nil

	case "get_public_profile":
		userID, _ := payload.Arguments["userId"].(string)
		if userID == "" {
			return nil, fmt.Errorf("userId is required")
		}
		var data any
		if err := s.getJSON(fmt.Sprintf("/api/public/profiles/%s", userID), &data); err != nil {
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

	case "list_writeups_for_challenge":
		slug, _ := payload.Arguments["slug"].(string)
		if slug == "" {
			return nil, fmt.Errorf("slug is required")
		}
		var data any
		if err := s.getJSON(fmt.Sprintf("/api/public/challenges/%s/writeups", slug), &data); err != nil {
			return nil, err
		}
		return mcpText(data), nil

	case "get_event":
		if err := s.requireEventKey(); err != nil {
			return nil, err
		}
		var data any
		if err := s.getJSONWithKey("/api/public/event", s.eventApiKey, &data); err != nil {
			return nil, err
		}
		return mcpText(data), nil

	case "update_event":
		if err := s.requireEventKey(); err != nil {
			return nil, err
		}
		body := map[string]any{}
		if title, _ := payload.Arguments["title"].(string); title != "" {
			body["title"] = title
		}
		if description, _ := payload.Arguments["description"].(string); description != "" {
			body["description"] = description
		}
		if len(body) == 0 {
			return nil, fmt.Errorf("title or description is required")
		}
		var data any
		if err := s.patchJSONWithKey("/api/public/event", body, s.eventApiKey, &data); err != nil {
			return nil, err
		}
		return mcpText(data), nil

	case "add_event_challenges":
		if err := s.requireEventKey(); err != nil {
			return nil, err
		}
		rawIDs, ok := payload.Arguments["challengeIds"].([]any)
		if !ok || len(rawIDs) == 0 {
			return nil, fmt.Errorf("challengeIds is required")
		}
		challengeIds := make([]string, 0, len(rawIDs))
		for _, raw := range rawIDs {
			id, _ := raw.(string)
			if id != "" {
				challengeIds = append(challengeIds, id)
			}
		}
		if len(challengeIds) == 0 {
			return nil, fmt.Errorf("challengeIds is required")
		}
		var data any
		if err := s.postJSONWithKey("/api/public/event/challenges", map[string]any{"challengeIds": challengeIds}, s.eventApiKey, &data); err != nil {
			return nil, err
		}
		return mcpText(data), nil

	case "remove_event_challenge":
		if err := s.requireEventKey(); err != nil {
			return nil, err
		}
		challengeId, _ := payload.Arguments["challengeId"].(string)
		if challengeId == "" {
			return nil, fmt.Errorf("challengeId is required")
		}
		var data any
		if err := s.deleteJSONWithKey(fmt.Sprintf("/api/public/event/challenges/%s", challengeId), s.eventApiKey, &data); err != nil {
			return nil, err
		}
		return mcpText(data), nil

	case "list_event_participants":
		if err := s.requireEventKey(); err != nil {
			return nil, err
		}
		var data any
		if err := s.getJSONWithKey("/api/public/event/participants", s.eventApiKey, &data); err != nil {
			return nil, err
		}
		return mcpText(data), nil

	default:
		return nil, fmt.Errorf("unknown tool: %s", payload.Name)
	}
}

func (s *mcpServer) getJSON(path string, out any) error {
	return s.requestJSON(http.MethodGet, path, nil, out, s.apiKey)
}

func (s *mcpServer) postJSON(path string, body any, out any) error {
	return s.requestJSON(http.MethodPost, path, body, out, s.apiKey)
}

func (s *mcpServer) getJSONWithKey(path, apiKey string, out any) error {
	return s.requestJSON(http.MethodGet, path, nil, out, apiKey)
}

func (s *mcpServer) postJSONWithKey(path string, body any, apiKey string, out any) error {
	return s.requestJSON(http.MethodPost, path, body, out, apiKey)
}

func (s *mcpServer) patchJSONWithKey(path string, body any, apiKey string, out any) error {
	return s.requestJSON(http.MethodPatch, path, body, out, apiKey)
}

func (s *mcpServer) deleteJSONWithKey(path, apiKey string, out any) error {
	return s.requestJSON(http.MethodDelete, path, nil, out, apiKey)
}

func (s *mcpServer) requestJSON(method, path string, body any, out any, apiKey string) error {
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

	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
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

func (s *mcpServer) requireEventKey() error {
	if strings.TrimSpace(s.eventApiKey) == "" {
		return fmt.Errorf("HACKLESS_EVENT_API_KEY is required for event tools")
	}
	return nil
}
