package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"

	"github.com/opsi-dev/opsi/cli/internal/cloudclient"
	"github.com/opsi-dev/opsi/cli/internal/config"
	"github.com/opsi-dev/opsi/cli/internal/keychain"
	"github.com/opsi-dev/opsi/cli/internal/repository"
)

type ServerOptions struct {
	Version          string
	Revision         string
	ConfigPath       string
	DefaultProjectID string
	RepoRoot         string
	KeychainFactory  func() (keychain.Store, error)
	HTTPClient       *http.Client
	GitRunner        repository.CommandRunner
	LogWriter        io.Writer
}

type Server struct {
	Version          string
	Revision         string
	ConfigPath       string
	DefaultProjectID string
	RepoRoot         string
	KeychainFactory  func() (keychain.Store, error)
	HTTPClient       *http.Client
	GitRunner        repository.CommandRunner
	SourceService    *SourceService
	LogWriter        io.Writer
	handlers         map[string]ToolHandler
	writeMu          sync.Mutex
}

func NewServer(opts ServerOptions) *Server {
	if opts.Version == "" {
		opts.Version = "dev"
	}
	if opts.KeychainFactory == nil {
		opts.KeychainFactory = func() (keychain.Store, error) { return keychain.NewOSStore() }
	}
	if opts.GitRunner == nil {
		opts.GitRunner = repository.ExecRunner{}
	}
	if opts.LogWriter == nil {
		opts.LogWriter = os.Stderr
	}

	s := &Server{
		Version:          opts.Version,
		Revision:         opts.Revision,
		ConfigPath:       opts.ConfigPath,
		DefaultProjectID: opts.DefaultProjectID,
		RepoRoot:         opts.RepoRoot,
		KeychainFactory:  opts.KeychainFactory,
		HTTPClient:       opts.HTTPClient,
		GitRunner:        opts.GitRunner,
		SourceService:    NewSourceService(opts.GitRunner),
		LogWriter:        opts.LogWriter,
	}
	s.handlers = s.registerHandlers()
	return s
}

func (s *Server) logf(format string, args ...any) {
	if s.LogWriter != nil {
		fmt.Fprintf(s.LogWriter, "[opsi-mcp] "+format+"\n", args...)
	}
}

func (s *Server) getCloudClient(ctx context.Context) (*cloudclient.Client, error) {
	cfg, err := config.LoadSelected(s.ConfigPath)
	if err != nil {
		return nil, &DomainError{Code: ErrCodeAuthorityUnavailable, Message: fmt.Sprintf("failed to load CLI config: %v", err)}
	}

	pat := ""
	if s.KeychainFactory != nil {
		store, storeErr := s.KeychainFactory()
		if storeErr == nil {
			val, _ := store.GetPAT()
			pat = val
		}
	}

	if strings.TrimSpace(pat) == "" {
		return nil, &DomainError{Code: ErrCodeAuthRequired, Message: "Opsi local session unauthenticated; run 'opsi login' outside MCP to authenticate"}
	}

	client, err := cloudclient.New(cfg.CloudURL, pat, s.Version, s.HTTPClient)
	if err != nil {
		return nil, &DomainError{Code: ErrCodeAuthorityUnavailable, Message: fmt.Sprintf("failed to initialize Cloud client: %v", err)}
	}
	return client, nil
}

// HandleMessage processes a single JSON-RPC message and returns the response (if any).
func (s *Server) HandleMessage(ctx context.Context, reqBytes []byte) (*JSONRPCResponse, error) {
	var req JSONRPCRequest
	if err := json.Unmarshal(reqBytes, &req); err != nil {
		return &JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      nil,
			Error: &JSONRPCError{
				Code:    JSONRPCParseError,
				Message: "Parse error",
			},
		}, nil
	}

	if req.Method == "" {
		return &JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &JSONRPCError{
				Code:    JSONRPCInvalidRequest,
				Message: "Invalid Request: method is required",
			},
		}, nil
	}

	// Handle notification
	if req.ID == nil {
		s.handleNotification(ctx, req)
		return nil, nil
	}

	switch req.Method {
	case "initialize":
		var params InitializeParams
		if len(req.Params) > 0 {
			_ = json.Unmarshal(req.Params, &params)
		}
		result := InitializeResult{
			ProtocolVersion: ProtocolVersion,
			Capabilities: ServerCapabilities{
				Tools:     &ToolsCapability{ListChanged: false},
				Resources: &ResourcesCapability{Subscribe: false, ListChanged: false},
			},
			ServerInfo: ServerInfo{
				Name:    ServerName,
				Version: s.Version,
			},
			Instructions: "Opsi MCP Server provides READ-ONLY access to project topology, source snapshots, deployment preflight, and dependency facts. Zero mutations are supported.",
		}
		return &JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  result,
		}, nil

	case "ping":
		return &JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result:  map[string]any{},
		}, nil

	case "tools/list":
		return &JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"tools": AllTools(),
			},
		}, nil

	case "tools/call":
		var params CallToolParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return &JSONRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error: &JSONRPCError{
					Code:    JSONRPCInvalidParams,
					Message: "Invalid params for tools/call",
				},
			}, nil
		}

		handler, ok := s.handlers[params.Name]
		if !ok {
			return &JSONRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result: CallToolResult{
					Content: []ContentItem{
						{
							Type: "text",
							Text: fmt.Sprintf(`{"code":"%s","message":"unknown tool %q"}`, ErrCodeNotFound, params.Name),
						},
					},
					IsError: true,
				},
			}, nil
		}

		output, err := handler(ctx, s, params.Arguments)
		if err != nil {
			var dErr *DomainError
			errCode := ErrCodeAuthorityUnavailable
			errMsg := err.Error()
			if errors.As(err, &dErr) {
				errCode = dErr.Code
				errMsg = dErr.Message
			}

			errPayload, _ := json.Marshal(ErrorResponse{Code: errCode, Message: errMsg})
			return &JSONRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result: CallToolResult{
					Content: []ContentItem{
						{
							Type: "text",
							Text: string(errPayload),
						},
					},
					IsError: true,
				},
			}, nil
		}

		jsonBytes, err := json.MarshalIndent(output, "", "  ")
		if err != nil {
			return &JSONRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error: &JSONRPCError{
					Code:    JSONRPCInternalError,
					Message: "Failed to encode response payload",
				},
			}, nil
		}

		return &JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: CallToolResult{
				Content: []ContentItem{
					{
						Type: "text",
						Text: string(jsonBytes),
					},
				},
				IsError: false,
			},
		}, nil

	case "resources/list":
		return &JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"resources": []Resource{
					{
						URI:         "opsi://project/context",
						Name:        "Project Context",
						Description: "Safe project facts and topology summary",
						MIMEType:    "application/json",
					},
				},
			},
		}, nil

	case "resources/read":
		var params ReadResourceParams
		if err := json.Unmarshal(req.Params, &params); err != nil {
			return &JSONRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Error: &JSONRPCError{
					Code:    JSONRPCInvalidParams,
					Message: "Invalid params for resources/read",
				},
			}, nil
		}

		if params.URI == "opsi://project/context" {
			ctxRes, err := s.handleProjectContext(ctx, s, map[string]any{})
			if err != nil {
				return &JSONRPCResponse{
					JSONRPC: "2.0",
					ID:      req.ID,
					Error: &JSONRPCError{
						Code:    JSONRPCInternalError,
						Message: err.Error(),
					},
				}, nil
			}
			data, _ := json.MarshalIndent(ctxRes, "", "  ")
			return &JSONRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result: ReadResourceResult{
					Contents: []ResourceContent{
						{
							URI:      params.URI,
							MIMEType: "application/json",
							Text:     string(data),
						},
					},
				},
			}, nil
		}

		return &JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &JSONRPCError{
				Code:    JSONRPCInvalidParams,
				Message: fmt.Sprintf("resource %q not found", params.URI),
			},
		}, nil

	default:
		return &JSONRPCResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Error: &JSONRPCError{
				Code:    JSONRPCMethodNotFound,
				Message: fmt.Sprintf("Method %q not found", req.Method),
			},
		}, nil
	}
}

func (s *Server) handleNotification(_ context.Context, req JSONRPCRequest) {
	switch req.Method {
	case "notifications/initialized", "initialized":
		s.logf("Client initialized session")
	default:
		s.logf("Ignored notification %q", req.Method)
	}
}
// ServeStdio starts the stdio JSON-RPC loop.
func (s *Server) ServeStdio(ctx context.Context, in io.Reader, out io.Writer) error {
	s.logf("Starting Opsi MCP Server (version %s) on stdio", s.Version)
	reader := bufio.NewReader(in)

	for {
		select {
		case <-ctx.Done():
			s.logf("Server context canceled, shutting down stdio transport")
			return ctx.Err()
		default:
		}

		line, err := reader.ReadString('\n')
		if err != nil {
			if errors.Is(err, io.EOF) {
				s.logf("Client closed stdin connection (EOF)")
				return nil
			}
			return err
		}

		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		// Handle optional Content-Length HTTP-style header
		if strings.HasPrefix(strings.ToLower(trimmed), "content-length:") {
			parts := strings.SplitN(trimmed, ":", 2)
			if len(parts) == 2 {
				length, lErr := strconv.Atoi(strings.TrimSpace(parts[1]))
				if lErr == nil && length > 0 {
					// Read next empty line
					for {
						sep, sErr := reader.ReadString('\n')
						if sErr != nil {
							return sErr
						}
						if strings.TrimSpace(sep) == "" {
							break
						}
					}
					body := make([]byte, length)
					if _, rErr := io.ReadFull(reader, body); rErr != nil {
						return rErr
					}
					trimmed = string(body)
				}
			}
		}

		resp, err := s.HandleMessage(ctx, []byte(trimmed))
		if err != nil {
			s.logf("Error handling request: %v", err)
			continue
		}
		if resp == nil {
			continue
		}

		respBytes, err := json.Marshal(resp)
		if err != nil {
			s.logf("Error encoding response: %v", err)
			continue
		}

		s.writeMu.Lock()
		_, _ = fmt.Fprintf(out, "%s\n", string(respBytes))
		s.writeMu.Unlock()
	}
}
