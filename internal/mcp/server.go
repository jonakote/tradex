// Package mcp exposes the safe, research-only Tradex surface over the
// Model Context Protocol (MCP) stdio transport.
//
// The server intentionally does not expose live broker mutations. Hermes can
// inspect the repository and run deterministic paper/research workflows, but
// live execution remains behind the existing broker safety gates and is not
// callable through this boundary.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"tradex/internal/experiment"
	"tradex/internal/reporting"
	"tradex/internal/research/replay"
)

const (
	ProtocolVersion = "2024-11-05"
	ServerName      = "tradex"
	ServerVersion   = "0.1.0"
)

type Server struct {
	workspace string
	specRoot  string
}

func NewServer(workspace string) (*Server, error) {
	return NewServerWithSpecRoot(workspace, "")
}

// NewServerWithSpecRoot adds a read-only Hermes specification tree to the
// resources exposed by the server. The specification root is deliberately
// separate from the writable Tradex workspace.
func NewServerWithSpecRoot(workspace, specRoot string) (*Server, error) {
	if workspace == "" {
		var err error
		workspace, err = os.Getwd()
		if err != nil {
			return nil, fmt.Errorf("resolve workspace: %w", err)
		}
	}
	abs, err := filepath.Abs(workspace)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace %q: %w", workspace, err)
	}
	abs, err = filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace symlinks: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, fmt.Errorf("inspect workspace: %w", err)
	}
	if !info.IsDir() {
		return nil, errors.New("MCP workspace must be a directory")
	}
	if specRoot != "" {
		specRoot, err = filepath.Abs(specRoot)
		if err != nil {
			return nil, fmt.Errorf("resolve Hermes specification root: %w", err)
		}
		specRoot, err = filepath.EvalSymlinks(specRoot)
		if err != nil {
			return nil, fmt.Errorf("resolve Hermes specification symlinks: %w", err)
		}
		specInfo, statErr := os.Stat(specRoot)
		if statErr != nil {
			return nil, fmt.Errorf("inspect Hermes specification root: %w", statErr)
		}
		if !specInfo.IsDir() {
			return nil, errors.New("Hermes specification root must be a directory")
		}
	}
	return &Server{workspace: abs, specRoot: specRoot}, nil
}

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type content struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type toolResult struct {
	Content           []content `json:"content"`
	IsError           bool      `json:"isError,omitempty"`
	StructuredContent any       `json:"structuredContent,omitempty"`
}

// Serve runs the MCP newline-delimited JSON-RPC transport until EOF or
// cancellation. Protocol output is written only to out; diagnostics belong on
// stderr in the command wrapper.
func (s *Server) Serve(ctx context.Context, in io.Reader, out io.Writer) error {
	decoder := json.NewDecoder(bufio.NewReader(in))
	encoder := json.NewEncoder(out)
	for {
		var req request
		if err := decoder.Decode(&req); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return fmt.Errorf("decode MCP request: %w", err)
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if req.JSONRPC != "2.0" || req.Method == "" {
			if !isNotification(req.ID) {
				if err := encoder.Encode(response{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32600, Message: "invalid request"}}); err != nil {
					return err
				}
			}
			continue
		}
		result, callErr := s.handle(ctx, req.Method, req.Params)
		if isNotification(req.ID) {
			continue
		}
		resp := response{JSONRPC: "2.0", ID: req.ID, Result: result}
		if callErr != nil {
			resp.Result = nil
			resp.Error = &rpcError{Code: callErr.code, Message: callErr.message, Data: callErr.data}
		}
		if err := encoder.Encode(resp); err != nil {
			return err
		}
	}
}

type callError struct {
	code    int
	message string
	data    any
}

func methodError(code int, message string, data any) *callError {
	return &callError{code: code, message: message, data: data}
}

func isNotification(id json.RawMessage) bool {
	return len(id) == 0 || string(id) == "null"
}

func (s *Server) handle(ctx context.Context, method string, params json.RawMessage) (any, *callError) {
	switch method {
	case "initialize":
		return map[string]any{
			"protocolVersion": ProtocolVersion,
			"capabilities": map[string]any{
				"tools":     map[string]any{},
				"resources": map[string]any{"subscribe": false, "listChanged": false},
			},
			"serverInfo": map[string]string{"name": ServerName, "version": ServerVersion},
		}, nil
	case "notifications/initialized", "ping":
		return map[string]any{}, nil
	case "tools/list":
		return map[string]any{"tools": toolDefinitions()}, nil
	case "resources/list":
		return map[string]any{"resources": s.resources()}, nil
	case "resources/read":
		return s.readResource(params)
	case "prompts/list":
		return map[string]any{"prompts": []any{}}, nil
	case "tools/call":
		return s.callTool(ctx, params)
	default:
		return nil, methodError(-32601, "method not found", method)
	}
}

func toolDefinitions() []map[string]any {
	return []map[string]any{
		{
			"name":        "tradex_status",
			"description": "Return safe repository and bot status. This is research/paper-only; no live broker mutations are exposed.",
			"inputSchema": objectSchema(nil, nil),
		},
		{
			"name":        "tradex_validate_artifact",
			"description": "Strictly validate a Tradex scenario, shared scenario, or frozen experiment manifest inside the configured workspace.",
			"inputSchema": objectSchema(map[string]any{
				"path": map[string]any{"type": "string", "description": "Workspace-relative JSON artifact path."},
				"kind": map[string]any{"type": "string", "enum": []string{"scenario", "shared_scenario", "manifest"}},
			}, []string{"path", "kind"}),
		},
		{
			"name":        "tradex_freeze_manifest",
			"description": "Create a new immutable manifest for a validated scenario or shared scenario. Existing files are never overwritten.",
			"inputSchema": objectSchema(map[string]any{
				"scenario_path": map[string]any{"type": "string"},
				"manifest_path": map[string]any{"type": "string"},
				"kind":          map[string]any{"type": "string", "enum": []string{"scenario", "shared_scenario"}},
			}, []string{"scenario_path", "manifest_path", "kind"}),
		},
		{
			"name":        "tradex_run_replay",
			"description": "Run one deterministic, manifest-verified paper replay and optionally save its result JSON. The audit path must be new.",
			"inputSchema": objectSchema(map[string]any{
				"scenario_path": map[string]any{"type": "string"},
				"manifest_path": map[string]any{"type": "string"},
				"audit_path":    map[string]any{"type": "string"},
				"result_path":   map[string]any{"type": "string", "description": "Optional new result JSON path."},
			}, []string{"scenario_path", "manifest_path", "audit_path"}),
		},
		{
			"name":        "tradex_run_shared_replay",
			"description": "Run a deterministic, manifest-verified multi-instrument paper replay and optionally save its result JSON.",
			"inputSchema": objectSchema(map[string]any{
				"scenario_path": map[string]any{"type": "string"},
				"manifest_path": map[string]any{"type": "string"},
				"audit_path":    map[string]any{"type": "string"},
				"result_path":   map[string]any{"type": "string"},
			}, []string{"scenario_path", "manifest_path", "audit_path"}),
		},
		{
			"name":        "tradex_build_report",
			"description": "Build and atomically export a self-verifying JSON and Markdown research report from a single or shared replay result.",
			"inputSchema": objectSchema(map[string]any{
				"result_path":          map[string]any{"type": "string"},
				"report_json_path":     map[string]any{"type": "string"},
				"report_markdown_path": map[string]any{"type": "string"},
				"kind":                 map[string]any{"type": "string", "enum": []string{"single", "shared"}},
			}, []string{"result_path", "report_json_path", "report_markdown_path", "kind"}),
		},
	}
}

func objectSchema(properties map[string]any, required []string) map[string]any {
	if properties == nil {
		properties = map[string]any{}
	}
	schema := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func (s *Server) callTool(ctx context.Context, params json.RawMessage) (any, *callError) {
	var call struct {
		Name      string                     `json:"name"`
		Arguments map[string]json.RawMessage `json:"arguments"`
	}
	if err := decodeParams(params, &call); err != nil {
		return nil, methodError(-32602, "invalid tools/call parameters", err.Error())
	}
	var value any
	var err error
	switch call.Name {
	case "tradex_status":
		value, err = s.status()
	case "tradex_validate_artifact":
		value, err = s.validateArtifact(call.Arguments)
	case "tradex_freeze_manifest":
		value, err = s.freezeManifest(call.Arguments)
	case "tradex_run_replay":
		value, err = s.runReplay(ctx, call.Arguments, false)
	case "tradex_run_shared_replay":
		value, err = s.runReplay(ctx, call.Arguments, true)
	case "tradex_build_report":
		value, err = s.buildReport(call.Arguments)
	default:
		return nil, methodError(-32602, "unknown tool", call.Name)
	}
	if err != nil {
		return toolError(err), nil
	}
	return toolResult{Content: []content{{Type: "text", Text: mustJSON(value)}}, StructuredContent: value}, nil
}

func toolError(err error) toolResult {
	return toolResult{IsError: true, Content: []content{{Type: "text", Text: err.Error()}}}
}

func decodeParams(raw json.RawMessage, dst any) error {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	return nil
}

func argString(args map[string]json.RawMessage, name string, required bool) (string, error) {
	raw, ok := args[name]
	if !ok {
		if required {
			return "", fmt.Errorf("missing required argument %q", name)
		}
		return "", nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil || strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("argument %q must be a non-empty string", name)
	}
	return value, nil
}

func (s *Server) workspacePath(input string, allowMissing bool) (string, error) {
	return safePath(s.workspace, input, allowMissing)
}

func (s *Server) specPath(input string) (string, error) {
	if s.specRoot == "" {
		return "", errors.New("Hermes specification root is not configured")
	}
	return safePath(s.specRoot, input, false)
}

func safePath(root, input string, allowMissing bool) (string, error) {
	if input == "" {
		return "", errors.New("path is required")
	}
	path := input
	if !filepath.IsAbs(path) {
		path = filepath.Join(root, path)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", errors.New("path must stay inside the configured workspace")
	}
	if info, statErr := os.Stat(abs); statErr == nil {
		if info.IsDir() {
			return "", errors.New("path must be a file")
		}
		resolved, evalErr := filepath.EvalSymlinks(abs)
		if evalErr != nil {
			return "", evalErr
		}
		resolvedRel, relErr := filepath.Rel(root, resolved)
		if relErr != nil || resolvedRel == ".." || strings.HasPrefix(resolvedRel, ".."+string(filepath.Separator)) {
			return "", errors.New("symlink target must stay inside the configured workspace")
		}
		return abs, nil
	} else if !os.IsNotExist(statErr) || !allowMissing {
		return "", statErr
	}
	parent := filepath.Dir(abs)
	parentInfo, err := os.Stat(parent)
	if err != nil {
		return "", fmt.Errorf("inspect output directory: %w", err)
	}
	if !parentInfo.IsDir() {
		return "", errors.New("output parent must be a directory")
	}
	resolvedParent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		return "", err
	}
	parentRel, err := filepath.Rel(root, resolvedParent)
	if err != nil || parentRel == ".." || strings.HasPrefix(parentRel, ".."+string(filepath.Separator)) {
		return "", errors.New("output directory must stay inside the configured workspace")
	}
	return abs, nil
}

func (s *Server) status() (map[string]any, error) {
	result := map[string]any{
		"server":                   ServerName,
		"server_version":           ServerVersion,
		"mcp_protocol_version":     ProtocolVersion,
		"workspace":                s.workspace,
		"mode":                     "research_and_paper_only",
		"live_order_tools_exposed": false,
		"documents":                []string{"README.md", "docs/TRADING_BOT_PLAN.md", "docs/IMPLEMENTATION_STATUS.md"},
		"api_key_values_exposed":   false,
		"hermes_spec_root":         s.specRoot,
		"hermes_specs_available":   s.specRoot != "",
	}
	cmd := exec.Command("git", "-C", s.workspace, "status", "--short", "--branch")
	if output, err := cmd.Output(); err == nil {
		lines := strings.Split(strings.TrimSpace(string(output)), "\n")
		result["git_status"] = lines
		result["git_repository"] = true
	} else {
		result["git_repository"] = false
	}
	return result, nil
}

func (s *Server) validateArtifact(args map[string]json.RawMessage) (map[string]any, error) {
	path, err := argString(args, "path", true)
	if err != nil {
		return nil, err
	}
	kind, err := argString(args, "kind", true)
	if err != nil {
		return nil, err
	}
	path, err = s.workspacePath(path, false)
	if err != nil {
		return nil, err
	}
	switch kind {
	case "scenario":
		scenario, err := replay.Load(path)
		if err != nil {
			return nil, err
		}
		manifest, err := replay.ManifestFor(scenario)
		if err != nil {
			return nil, err
		}
		return map[string]any{"valid": true, "kind": kind, "scenario": scenario.Name, "symbol": scenario.Instrument.Symbol, "events": len(scenario.Events), "manifest_sha256": manifest.ManifestSHA256}, nil
	case "shared_scenario":
		scenario, err := replay.LoadShared(path)
		if err != nil {
			return nil, err
		}
		manifest, err := replay.SharedManifestFor(scenario)
		if err != nil {
			return nil, err
		}
		return map[string]any{"valid": true, "kind": kind, "scenario": scenario.Name, "legs": len(scenario.Legs), "manifest_sha256": manifest.ManifestSHA256}, nil
	case "manifest":
		manifest, err := experiment.Load(path)
		if err != nil {
			return nil, err
		}
		return map[string]any{"valid": true, "kind": kind, "experiment_id": manifest.ExperimentID, "manifest_sha256": manifest.ManifestSHA256}, nil
	default:
		return nil, fmt.Errorf("unsupported artifact kind %q", kind)
	}
}

func (s *Server) freezeManifest(args map[string]json.RawMessage) (map[string]any, error) {
	scenarioPath, err := argString(args, "scenario_path", true)
	if err != nil {
		return nil, err
	}
	manifestPath, err := argString(args, "manifest_path", true)
	if err != nil {
		return nil, err
	}
	kind, err := argString(args, "kind", true)
	if err != nil {
		return nil, err
	}
	scenarioPath, err = s.workspacePath(scenarioPath, false)
	if err != nil {
		return nil, err
	}
	manifestPath, err = s.workspacePath(manifestPath, true)
	if err != nil {
		return nil, err
	}
	var manifest experiment.Manifest
	switch kind {
	case "scenario":
		scenario, loadErr := replay.Load(scenarioPath)
		if loadErr != nil {
			return nil, loadErr
		}
		manifest, err = replay.ManifestFor(scenario)
	case "shared_scenario":
		scenario, loadErr := replay.LoadShared(scenarioPath)
		if loadErr != nil {
			return nil, loadErr
		}
		manifest, err = replay.SharedManifestFor(scenario)
	default:
		return nil, fmt.Errorf("unsupported artifact kind %q", kind)
	}
	if err != nil {
		return nil, err
	}
	if err := experiment.WriteNew(manifestPath, manifest); err != nil {
		return nil, err
	}
	return map[string]any{"created": true, "kind": kind, "manifest_path": manifestPath, "experiment_id": manifest.ExperimentID, "manifest_sha256": manifest.ManifestSHA256}, nil
}

func (s *Server) runReplay(ctx context.Context, args map[string]json.RawMessage, shared bool) (map[string]any, error) {
	scenarioPath, err := argString(args, "scenario_path", true)
	if err != nil {
		return nil, err
	}
	manifestPath, err := argString(args, "manifest_path", true)
	if err != nil {
		return nil, err
	}
	auditPath, err := argString(args, "audit_path", true)
	if err != nil {
		return nil, err
	}
	resultPath, err := argString(args, "result_path", false)
	if err != nil {
		return nil, err
	}
	scenarioPath, err = s.workspacePath(scenarioPath, false)
	if err != nil {
		return nil, err
	}
	manifestPath, err = s.workspacePath(manifestPath, false)
	if err != nil {
		return nil, err
	}
	auditPath, err = s.workspacePath(auditPath, true)
	if err != nil {
		return nil, err
	}
	if resultPath != "" {
		resultPath, err = s.workspacePath(resultPath, true)
		if err != nil {
			return nil, err
		}
	}
	manifest, err := experiment.Load(manifestPath)
	if err != nil {
		return nil, err
	}
	if shared {
		scenario, loadErr := replay.LoadShared(scenarioPath)
		if loadErr != nil {
			return nil, loadErr
		}
		result, runErr := replay.RunSharedVerified(ctx, scenario, manifest, auditPath)
		if runErr != nil {
			return nil, runErr
		}
		if resultPath != "" {
			if err := writeJSONNew(resultPath, result); err != nil {
				return nil, err
			}
		}
		report, err := reporting.FromShared(result)
		if err != nil {
			return nil, err
		}
		return replaySummary(report, result.Scenario, manifest.ManifestSHA256, auditPath, resultPath), nil
	}
	scenario, loadErr := replay.Load(scenarioPath)
	if loadErr != nil {
		return nil, loadErr
	}
	result, runErr := replay.RunVerified(ctx, scenario, manifest, auditPath)
	if runErr != nil {
		return nil, runErr
	}
	if resultPath != "" {
		if err := writeJSONNew(resultPath, result); err != nil {
			return nil, err
		}
	}
	report, err := reporting.FromSingle(result)
	if err != nil {
		return nil, err
	}
	return replaySummary(report, result.Scenario, manifest.ManifestSHA256, auditPath, resultPath), nil
}

func replaySummary(report reporting.Report, scenario, manifestHash, auditPath, resultPath string) map[string]any {
	return map[string]any{
		"completed":       true,
		"scenario":        scenario,
		"manifest_sha256": manifestHash,
		"audit_path":      auditPath,
		"result_path":     resultPath,
		"report": map[string]any{
			"mode": report.Mode, "trades": report.Account.Trades, "wins": report.Account.Wins,
			"losses": report.Account.Losses, "win_rate": report.Account.WinRate,
			"net_profit_minor": report.Account.NetProfitMinor, "return_bps": report.Account.ReturnBPS,
			"max_drawdown_minor": report.Account.MaxDrawdownMinor,
			"decisions":          report.Decisions.Total, "approved": report.Decisions.Approved, "rejected": report.Decisions.Rejected,
		},
	}
}

func (s *Server) buildReport(args map[string]json.RawMessage) (map[string]any, error) {
	resultPath, err := argString(args, "result_path", true)
	if err != nil {
		return nil, err
	}
	jsonPath, err := argString(args, "report_json_path", true)
	if err != nil {
		return nil, err
	}
	markdownPath, err := argString(args, "report_markdown_path", true)
	if err != nil {
		return nil, err
	}
	kind, err := argString(args, "kind", true)
	if err != nil {
		return nil, err
	}
	resultPath, err = s.workspacePath(resultPath, false)
	if err != nil {
		return nil, err
	}
	jsonPath, err = s.workspacePath(jsonPath, true)
	if err != nil {
		return nil, err
	}
	markdownPath, err = s.workspacePath(markdownPath, true)
	if err != nil {
		return nil, err
	}
	var report reporting.Report
	switch kind {
	case "single":
		var result replay.Result
		if err := loadJSON(resultPath, &result); err != nil {
			return nil, err
		}
		report, err = reporting.FromSingle(result)
	case "shared":
		var result replay.SharedResult
		if err := loadJSON(resultPath, &result); err != nil {
			return nil, err
		}
		report, err = reporting.FromShared(result)
	default:
		return nil, fmt.Errorf("unsupported report kind %q", kind)
	}
	if err != nil {
		return nil, err
	}
	if err := reporting.WriteBundleNew(jsonPath, markdownPath, report); err != nil {
		return nil, err
	}
	return map[string]any{"created": true, "kind": kind, "report_json_path": jsonPath, "report_markdown_path": markdownPath, "report_sha256": report.ReportSHA256, "experiment_id": report.ExperimentID}, nil
}

func loadJSON(path string, dst any) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	decoder := json.NewDecoder(file)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return errors.New("JSON artifact contains trailing data")
		}
		return err
	}
	return nil
}

func writeJSONNew(path string, value any) (writeErr error) {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := file.Close(); writeErr == nil && closeErr != nil {
			writeErr = closeErr
		}
	}()
	if _, err := file.Write(encoded); err != nil {
		return err
	}
	return file.Sync()
}

func (s *Server) resources() []map[string]any {
	resources := []map[string]any{
		{"uri": "tradex://README.md", "name": "README", "description": "Tradex operating overview and CLI workflows", "mimeType": "text/markdown"},
		{"uri": "tradex://docs/TRADING_BOT_PLAN.md", "name": "Trading bot plan", "description": "Strategy, risk, execution, and research plan", "mimeType": "text/markdown"},
		{"uri": "tradex://docs/IMPLEMENTATION_STATUS.md", "name": "Implementation status", "description": "Implemented milestones and verification status", "mimeType": "text/markdown"},
		{"uri": "tradex://docs/TRIANGLE_PATTERN_SPEC.md", "name": "Triangle pattern specification", "description": "Versioned triangle geometry and breakout research contract", "mimeType": "text/markdown"},
	}
	if s.specRoot == "" {
		return resources
	}
	_ = filepath.WalkDir(s.specRoot, func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() || strings.ToLower(filepath.Ext(entry.Name())) != ".md" {
			return nil
		}
		rel, err := filepath.Rel(s.specRoot, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		resources = append(resources, map[string]any{
			"uri":         "hermes://" + rel,
			"name":        "Hermes spec: " + rel,
			"description": "Read-only Hermes specification document",
			"mimeType":    "text/markdown",
		})
		return nil
	})
	return resources
}

func (s *Server) readResource(params json.RawMessage) (map[string]any, *callError) {
	var input struct {
		URI string `json:"uri"`
	}
	if err := decodeParams(params, &input); err != nil || input.URI == "" {
		return nil, methodError(-32602, "resources/read requires a uri", nil)
	}
	allowed := map[string]string{
		"tradex://README.md":                     "README.md",
		"tradex://docs/TRADING_BOT_PLAN.md":      "docs/TRADING_BOT_PLAN.md",
		"tradex://docs/IMPLEMENTATION_STATUS.md": "docs/IMPLEMENTATION_STATUS.md",
		"tradex://docs/TRIANGLE_PATTERN_SPEC.md": "docs/TRIANGLE_PATTERN_SPEC.md",
	}
	var path string
	var err error
	if relative, ok := allowed[input.URI]; ok {
		path, err = s.workspacePath(relative, false)
	} else if strings.HasPrefix(input.URI, "hermes://") {
		if s.specRoot == "" {
			return nil, methodError(-32004, "Hermes specification root is not configured", nil)
		}
		relative := strings.TrimPrefix(input.URI, "hermes://")
		if relative == "" || strings.ToLower(filepath.Ext(relative)) != ".md" {
			return nil, methodError(-32004, "resource not found", input.URI)
		}
		path, err = s.specPath(filepath.FromSlash(relative))
	} else {
		return nil, methodError(-32004, "resource not found", input.URI)
	}
	if err != nil {
		return nil, methodError(-32004, err.Error(), nil)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, methodError(-32004, "read resource failed", err.Error())
	}
	return map[string]any{"contents": []map[string]any{{"uri": input.URI, "mimeType": "text/markdown", "text": string(data)}}}, nil
}

func mustJSON(value any) string {
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return fmt.Sprintf("%v", value)
	}
	return string(encoded)
}
