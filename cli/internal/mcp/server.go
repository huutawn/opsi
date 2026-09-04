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
	"regexp"
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
		return nil, &DomainError{
			Code:       ErrCodeAuthorityUnavailable,
			Message:    "failed to load CLI config",
			Retryable:  false,
			NextAction: "Verify that the CLI configuration file exists and is valid.",
		}
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
		return nil, &DomainError{
			Code:       ErrCodeAuthRequired,
			Message:    "Opsi local session unauthenticated; run 'opsi login' outside MCP to authenticate",
			Retryable:  false,
			NextAction: "Run 'opsi login' outside MCP to authenticate.",
		}
	}

	client, err := cloudclient.New(cfg.CloudURL, pat, s.Version, s.HTTPClient)
	if err != nil {
		return nil, &DomainError{
			Code:       ErrCodeAuthorityUnavailable,
			Message:    "failed to initialize Cloud client",
			Retryable:  false,
			NextAction: "Verify Cloud URL configuration in your CLI config.",
		}
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
			structured := &ErrorResponse{
				Code:       ErrCodeNotFound,
				Message:    fmt.Sprintf("unknown tool %q", sanitizeDiagnostic(params.Name)),
				Retryable:  false,
				NextAction: "Call tools/list to see available Opsi tools.",
			}
			errPayload, _ := json.Marshal(structured)
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
					StructuredContent: structured,
					IsError:           true,
				},
			}, nil
		}

		output, err := handler(ctx, s, params.Arguments)
		if err != nil {
			return &JSONRPCResponse{
				JSONRPC: "2.0",
				ID:      req.ID,
				Result:  s.formatToolError(err),
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

func (s *Server) formatToolError(err error) CallToolResult {
	var dErr *DomainError
	code := ErrCodeAuthorityUnavailable
	message := "tool execution failed"
	retryable := false
	nextAction := ""

	if errors.As(err, &dErr) {
		code = dErr.Code
		message = dErr.Message
		retryable = dErr.Retryable
		nextAction = dErr.NextAction
	} else if err != nil {
		message = err.Error()
	}

	message = sanitizeDiagnostic(message)
	if nextAction == "" {
		nextAction = defaultNextAction(code)
	}

	structured := &ErrorResponse{
		Code:       code,
		Message:    message,
		Retryable:  retryable,
		NextAction: nextAction,
	}

	errPayload, _ := json.Marshal(structured)
	return CallToolResult{
		Content: []ContentItem{
			{
				Type: "text",
				Text: string(errPayload),
			},
		},
		StructuredContent: structured,
		IsError:           true,
	}
}

func defaultNextAction(code string) string {
	switch code {
	case ErrCodeAuthRequired:
		return "Run 'opsi login' outside MCP to authenticate."
	case ErrCodeForbidden:
		return "Check your user role or project permissions."
	case ErrCodeNotFound:
		return "Verify that the requested resource exists."
	case ErrCodeAmbiguousProject:
		return "Specify project_id explicitly in tool arguments."
	case ErrCodeAuthorityUnavailable:
		return "Verify network connectivity or check Cloud service status."
	case ErrCodeLimitExceeded:
		return "Narrow query parameters or reduce batch size."
	case ErrCodeInvalidArgument:
		return "Check tool arguments and try again."
	default:
		return "Check tool parameters and project state."
	}
}

var (
	patRegex     = regexp.MustCompile(`(?i)\bopsi_pat_[a-zA-Z0-9_-]+`)
	tokenRegex   = regexp.MustCompile(`(?i)\b(?:opsi_(?:pat|agent_token)|ghp_[a-zA-Z0-9]+|github_pat_[a-zA-Z0-9_]+)\b`)
	bearerRegex  = regexp.MustCompile(`(?i)Bearer\s+[a-zA-Z0-9_.\-]+`)
	authHdrRegex = regexp.MustCompile(`(?i)(?:authorization|proxy-authorization):\s*[^\r\n]+`)
	headerRegex  = regexp.MustCompile(`(?i)(?:[A-Za-z0-9_-]+-Token|[A-Za-z0-9_-]+-Key):\s*[^\r\n]+`)
	credURIRegex = regexp.MustCompile(`([a-zA-Z0-9+.-]+://)([^/\s:@]*):([^/\s:@]+)@([^\s"'\` + "`" + `]+)`)
	credEnvRegex = regexp.MustCompile(`(?i)(password|secret|token|api_key|pat)\s*[:=]\s*["']?([^\s"',;]+)["']?`)
	privKeyRegex = regexp.MustCompile(`(?s)-----BEGIN (?:[A-Z ]+ )?PRIVATE KEY-----.*?-----END (?:[A-Z ]+ )?PRIVATE KEY-----`)
)

func sanitizeDiagnostic(input string) string {
	if input == "" {
		return ""
	}
	out := privKeyRegex.ReplaceAllString(input, "[REDACTED_PRIVATE_KEY]")
	out = authHdrRegex.ReplaceAllString(out, "Authorization: [REDACTED]")
	out = headerRegex.ReplaceAllString(out, "Header: [REDACTED]")
	out = bearerRegex.ReplaceAllString(out, "Bearer [REDACTED]")
	out = patRegex.ReplaceAllString(out, "[REDACTED_PAT]")
	out = tokenRegex.ReplaceAllString(out, "[REDACTED_TOKEN]")
	out = credURIRegex.ReplaceAllString(out, "$1$2:[REDACTED]@$4")
	out = credEnvRegex.ReplaceAllString(out, "$1=[REDACTED]")
	return strings.TrimSpace(out)
}
