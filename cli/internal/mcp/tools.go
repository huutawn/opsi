package mcp

func AllTools() []Tool {
	return []Tool{
		{
			Name:        "project_context",
			Description: "Get safe high-level project summary facts, counts, topology revision, and deployment summary (read-only).",
			InputSchema: ToolInputSchema{
				Type: "object",
				Properties: map[string]PropertyDoc{
					"project_id": {
						Type:        "string",
						Description: "Optional project ID. Inferred from active session if omitted and unambiguous.",
					},
					"environment_id": {
						Type:        "string",
						Description: "Optional environment ID filter.",
					},
				},
			},
		},
		{
			Name:        "topology",
			Description: "Get canonical factual applied topology plan for a project (read-only).",
			InputSchema: ToolInputSchema{
				Type: "object",
				Properties: map[string]PropertyDoc{
					"project_id": {
						Type:        "string",
						Description: "Optional project ID. Inferred from active session if omitted.",
					},
				},
			},
		},
		{
			Name:        "applications_list",
			Description: "List applications with safe metadata, source bindings, build strategies, and current build records (read-only).",
			InputSchema: ToolInputSchema{
				Type: "object",
				Properties: map[string]PropertyDoc{
					"project_id": {
						Type:        "string",
						Description: "Optional project ID. Inferred from active session if omitted.",
					},
					"limit": {
						Type:        "integer",
						Description: "Maximum number of applications to return (default 50, max 100).",
					},
				},
			},
		},
		{
			Name:        "application_get",
			Description: "Get detailed safe facts for a single application including configuration, source binding, and dependency summaries (read-only).",
			InputSchema: ToolInputSchema{
				Type: "object",
				Properties: map[string]PropertyDoc{
					"application_id": {
						Type:        "string",
						Description: "The ID or name of the application to inspect.",
					},
					"project_id": {
						Type:        "string",
						Description: "Optional project ID. Inferred from active session if omitted.",
					},
				},
				Required: []string{"application_id"},
			},
		},
		{
			Name:        "application_dependencies",
			Description: "List ADC-01 dependency contracts and safe realization facts for an application (read-only).",
			InputSchema: ToolInputSchema{
				Type: "object",
				Properties: map[string]PropertyDoc{
					"application_id": {
						Type:        "string",
						Description: "The ID or name of the consuming application.",
					},
					"project_id": {
						Type:        "string",
						Description: "Optional project ID. Inferred from active session if omitted.",
					},
				},
				Required: []string{"application_id"},
			},
		},
		{
			Name:        "managed_resources_list",
			Description: "List managed resources (PostgreSQL, Valkey, etc.) with safe metadata (read-only).",
			InputSchema: ToolInputSchema{
				Type: "object",
				Properties: map[string]PropertyDoc{
					"project_id": {
						Type:        "string",
						Description: "Optional project ID. Inferred from active session if omitted.",
					},
					"environment_id": {
						Type:        "string",
						Description: "Optional environment ID filter.",
					},
					"limit": {
						Type:        "integer",
						Description: "Maximum number of resources to return (default 50, max 100).",
					},
				},
			},
		},
		{
			Name:        "managed_resource_get",
			Description: "Get safe details for a single managed resource (read-only).",
			InputSchema: ToolInputSchema{
				Type: "object",
				Properties: map[string]PropertyDoc{
					"resource_id": {
						Type:        "string",
						Description: "The ID of the managed resource.",
					},
					"project_id": {
						Type:        "string",
						Description: "Optional project ID. Inferred from active session if omitted.",
					},
				},
				Required: []string{"resource_id"},
			},
		},
		{
			Name:        "build_records_list",
			Description: "List immutable BuildRecords with provenance and dependency freshness facts (read-only).",
			InputSchema: ToolInputSchema{
				Type: "object",
				Properties: map[string]PropertyDoc{
					"project_id": {
						Type:        "string",
						Description: "Optional project ID. Inferred from active session if omitted.",
					},
					"service_key": {
						Type:        "string",
						Description: "Optional service key filter.",
					},
					"sha": {
						Type:        "string",
						Description: "Optional git commit SHA filter.",
					},
					"status": {
						Type:        "string",
						Description: "Optional status filter (e.g. accepted, failed).",
					},
					"limit": {
						Type:        "integer",
						Description: "Maximum number of build records to return (default 50, max 100).",
					},
					"cursor": {
						Type:        "string",
						Description: "Pagination cursor.",
					},
				},
			},
		},
		{
			Name:        "build_record_get",
			Description: "Get detailed facts for an immutable BuildRecord (read-only).",
			InputSchema: ToolInputSchema{
				Type: "object",
				Properties: map[string]PropertyDoc{
					"build_record_id": {
						Type:        "string",
						Description: "The BuildRecord ID to inspect.",
					},
					"project_id": {
						Type:        "string",
						Description: "Optional project ID. Inferred from active session if omitted.",
					},
				},
				Required: []string{"build_record_id"},
			},
		},
		{
			Name:        "deployments_list",
			Description: "List deployment jobs with rollout state and outcomes (read-only). Note: Deployment outcome ≠ runtime health.",
			InputSchema: ToolInputSchema{
				Type: "object",
				Properties: map[string]PropertyDoc{
					"project_id": {
						Type:        "string",
						Description: "Optional project ID. Inferred from active session if omitted.",
					},
					"service_id": {
						Type:        "string",
						Description: "Optional service ID filter.",
					},
					"environment_id": {
						Type:        "string",
						Description: "Optional environment ID filter.",
					},
					"limit": {
						Type:        "integer",
						Description: "Maximum number of deployments to return (default 50, max 100).",
					},
				},
			},
		},
		{
			Name:        "deployment_get",
			Description: "Get details of a specific deployment job (read-only).",
			InputSchema: ToolInputSchema{
				Type: "object",
				Properties: map[string]PropertyDoc{
					"deployment_id": {
						Type:        "string",
						Description: "The DeploymentJob ID to inspect.",
					},
					"project_id": {
						Type:        "string",
						Description: "Optional project ID. Inferred from active session if omitted.",
					},
				},
				Required: []string{"deployment_id"},
			},
		},
		{
			Name:        "deployment_preflight",
			Description: "Evaluate ADC-04 deployment preflight checks without mutation (read-only).",
			InputSchema: ToolInputSchema{
				Type: "object",
				Properties: map[string]PropertyDoc{
					"service_id": {
						Type:        "string",
						Description: "The ID or name of the service to preflight.",
					},
					"build_record_id": {
						Type:        "string",
						Description: "The ID of the accepted BuildRecord to deploy.",
					},
					"environment_id": {
						Type:        "string",
						Description: "Optional environment ID.",
					},
					"runtime_id": {
						Type:        "string",
						Description: "Optional target runtime ID.",
					},
					"project_id": {
						Type:        "string",
						Description: "Optional project ID. Inferred from active session if omitted.",
					},
				},
				Required: []string{"service_id", "build_record_id"},
			},
		},
		{
			Name:        "source_risk_report",
			Description: "Get ADC-05 source risk analysis report with heuristic findings and redacted credentials (read-only).",
			InputSchema: ToolInputSchema{
				Type: "object",
				Properties: map[string]PropertyDoc{
					"application_id": {
						Type:        "string",
						Description: "The ID or name of the application.",
					},
					"commit_sha": {
						Type:        "string",
						Description: "Optional git commit SHA. Defaults to active application commit.",
					},
					"report_id": {
						Type:        "string",
						Description: "Optional specific report ID.",
					},
					"project_id": {
						Type:        "string",
						Description: "Optional project ID. Inferred from active session if omitted.",
					},
				},
				Required: []string{"application_id"},
			},
		},
		{
			Name:        "dependency_verification_latest",
			Description: "Get latest ADC-05 5-layer dependency verification run result (read-only).",
			InputSchema: ToolInputSchema{
				Type: "object",
				Properties: map[string]PropertyDoc{
					"dependency_logical_name": {
						Type:        "string",
						Description: "Logical name of the dependency contract.",
					},
					"application_id": {
						Type:        "string",
						Description: "Optional consumer application ID.",
					},
					"environment_id": {
						Type:        "string",
						Description: "Optional environment ID.",
					},
					"project_id": {
						Type:        "string",
						Description: "Optional project ID. Inferred from active session if omitted.",
					},
				},
				Required: []string{"dependency_logical_name"},
			},
		},
		{
			Name:        "dependency_verification_history",
			Description: "List historical dependency verification runs for a deployment (read-only).",
			InputSchema: ToolInputSchema{
				Type: "object",
				Properties: map[string]PropertyDoc{
					"deployment_job_id": {
						Type:        "string",
						Description: "Deployment job ID to retrieve verification history for.",
					},
					"project_id": {
						Type:        "string",
						Description: "Optional project ID. Inferred from active session if omitted.",
					},
				},
				Required: []string{"deployment_job_id"},
			},
		},
		{
			Name:        "source_files_list",
			Description: "List source files inside ApplicationRoot for an exact commit SHA or BuildRecord (read-only).",
			InputSchema: ToolInputSchema{
				Type: "object",
				Properties: map[string]PropertyDoc{
					"application_id": {
						Type:        "string",
						Description: "The ID or name of the application.",
					},
					"build_record_id": {
						Type:        "string",
						Description: "Optional BuildRecord ID to bind to its exact source commit.",
					},
					"commit_sha": {
						Type:        "string",
						Description: "Optional exact commit SHA. If omitted, uses BuildRecord or active application commit.",
					},
					"path_prefix": {
						Type:        "string",
						Description: "Optional path prefix relative to ApplicationRoot.",
					},
					"limit": {
						Type:        "integer",
						Description: "Maximum number of files to return (default 50, max 200).",
					},
					"cursor": {
						Type:        "string",
						Description: "Pagination cursor.",
					},
					"project_id": {
						Type:        "string",
						Description: "Optional project ID. Inferred from active session if omitted.",
					},
				},
				Required: []string{"application_id"},
			},
		},
		{
			Name:        "source_file_read",
			Description: "Read exact content of a source file bound to a specific commit SHA or BuildRecord (read-only).",
			InputSchema: ToolInputSchema{
				Type: "object",
				Properties: map[string]PropertyDoc{
					"application_id": {
						Type:        "string",
						Description: "The ID or name of the application.",
					},
					"relative_path": {
						Type:        "string",
						Description: "Relative path inside ApplicationRoot (no '..', no leading '/').",
					},
					"build_record_id": {
						Type:        "string",
						Description: "Optional BuildRecord ID to bind to its exact source commit.",
					},
					"commit_sha": {
						Type:        "string",
						Description: "Optional exact commit SHA.",
					},
					"max_bytes": {
						Type:        "integer",
						Description: "Maximum bytes to read (default 65536, max 262144).",
					},
					"project_id": {
						Type:        "string",
						Description: "Optional project ID. Inferred from active session if omitted.",
					},
				},
				Required: []string{"application_id", "relative_path"},
			},
		},
		{
			Name:        "source_search",
			Description: "Deterministic bounded literal text search across source files inside ApplicationRoot at an exact commit SHA (read-only).",
			InputSchema: ToolInputSchema{
				Type: "object",
				Properties: map[string]PropertyDoc{
					"application_id": {
						Type:        "string",
						Description: "The ID or name of the application.",
					},
					"query": {
						Type:        "string",
						Description: "Literal text to search (min 2 chars, max 128 chars).",
					},
					"path_prefix": {
						Type:        "string",
						Description: "Optional path prefix relative to ApplicationRoot.",
					},
					"build_record_id": {
						Type:        "string",
						Description: "Optional BuildRecord ID to bind to its exact source commit.",
					},
					"commit_sha": {
						Type:        "string",
						Description: "Optional exact commit SHA.",
					},
					"limit": {
						Type:        "integer",
						Description: "Maximum number of matches to return (default 20, max 50).",
					},
					"project_id": {
						Type:        "string",
						Description: "Optional project ID. Inferred from active session if omitted.",
					},
				},
				Required: []string{"application_id", "query"},
			},
		},
	}
}
