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
	"net/url"
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
	baseURL string
	cookie  string
	apiKey  string
	client  *http.Client
}

func main() {
	httpMode := flag.Bool("http", false, "run as a local HTTP test server instead of stdio MCP")
	addr := flag.String("addr", ":8000", "HTTP listen address")
	baseURLFlag := flag.String("base-url", "", "Hackless API base URL")
	flag.Parse()

	baseURL := resolveBaseURL(*baseURLFlag, flag.Args())
	srv := &mcpServer{
		baseURL: baseURL,
		cookie:  env("HACKLESS_COOKIE", ""),
		apiKey:  env("HACKLESS_API_KEY", ""),
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
			"message": "Hackless MCP test endpoint. Use POST /mcp with JSON-RPC.",
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
					"version": "0.1.2",
				},
				"capabilities": map[string]any{
					"tools": map[string]any{"listChanged": false},
				},
			},
		}
	case "tools/list":
		return jsonRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"tools": []toolDef{
					{
						Name:        "health",
						Description: "Check whether the public Hackless API is healthy.",
						InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
					},
					{
						Name:        "list_challenges",
						Description: "List public Hackless challenges.",
						InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
					},
					{
						Name:        "get_challenge",
						Description: "Get a specific public challenge by slug.",
						InputSchema: map[string]any{
							"type": "object",
							"properties": map[string]any{
								"slug": map[string]any{"type": "string"},
							},
							"required": []string{"slug"},
						},
					},
					{
						Name:        "get_my_progress",
						Description: "Get the authenticated user's profile and progress.",
						InputSchema: map[string]any{"type": "object", "properties": map[string]any{}},
					},
					{
						Name:        "get_public_profile",
						Description: "Get a public profile by user ID.",
						InputSchema: map[string]any{
							"type": "object",
							"properties": map[string]any{
								"userId": map[string]any{"type": "string"},
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
						Name:        "list_writeups_for_challenge",
						Description: "List writeups for a solved challenge.",
						InputSchema: map[string]any{
							"type": "object",
							"properties": map[string]any{
								"slug": map[string]any{"type": "string"},
							},
							"required": []string{"slug"},
						},
					},
					{
						Name:        "submit_flag",
						Description: "Submit a flag for a challenge.",
						InputSchema: map[string]any{
							"type": "object",
							"properties": map[string]any{
								"slug": map[string]any{"type": "string"},
								"flag": map[string]any{"type": "string"},
							},
							"required": []string{"slug", "flag"},
						},
					},
				},
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

func (s *mcpServer) callTool(params json.RawMessage) (any, error) {
	var payload struct {
		Name      string                 `json:"name"`
		Arguments map[string]any         `json:"arguments"`
	}
	if err := json.Unmarshal(params, &payload); err != nil {
		return nil, fmt.Errorf("invalid params: %w", err)
	}

	switch payload.Name {
	case "health":
		var data any
		if err := s.getJSON("/api/public/health", &data); err != nil {
			return nil, err
		}
		return mcpText(data), nil
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
		if err := s.getJSON("/api/public/challenges/"+url.PathEscape(slug), &data); err != nil {
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
		if err := s.getJSON("/api/public/profiles/"+url.PathEscape(userID), &data); err != nil {
			return nil, err
		}
		return mcpText(data), nil
	case "view_leaderboard":
		var data any
		if err := s.getJSON("/api/public/leaderboard", &data); err != nil {
			return nil, err
		}
		return mcpText(data), nil
	case "list_writeups_for_challenge":
		slug, _ := payload.Arguments["slug"].(string)
		if slug == "" {
			return nil, fmt.Errorf("slug is required")
		}
		var data any
		if err := s.getJSON("/api/public/challenges/"+url.PathEscape(slug)+"/writeups", &data); err != nil {
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
		if err := s.postJSON(fmt.Sprintf("/api/public/challenges/%s/submit", url.PathEscape(slug)), map[string]string{"flag": flag}, &data); err != nil {
			return nil, err
		}
		return mcpText(data), nil
	default:
		return nil, fmt.Errorf("unknown tool: %s", payload.Name)
	}
}

func (s *mcpServer) getJSON(path string, out any) error {
	return s.requestJSON(http.MethodGet, path, nil, out)
}

func (s *mcpServer) postJSON(path string, body any, out any) error {
	return s.requestJSON(http.MethodPost, path, body, out)
}

func (s *mcpServer) requestJSON(method, path string, body any, out any) error {
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
	req.Header.Set("Content-Type", "application/json")
	if s.apiKey != "" {
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
