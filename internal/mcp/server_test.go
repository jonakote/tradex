package mcp

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestServeInitializeToolsAndResources(t *testing.T) {
	server, err := NewServer(".")
	if err != nil {
		t.Fatal(err)
	}
	input := strings.NewReader("{" + `"jsonrpc":"2.0","id":1,"method":"initialize","params":{}` + "}\n" +
		"{" + `"jsonrpc":"2.0","id":2,"method":"tools/list"` + "}\n" +
		"{" + `"jsonrpc":"2.0","id":3,"method":"resources/list"` + "}\n")
	var output strings.Builder
	if err := server.Serve(context.Background(), input, &output); err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(strings.NewReader(output.String()))
	var first map[string]any
	if err := decoder.Decode(&first); err != nil {
		t.Fatal(err)
	}
	if first["jsonrpc"] != "2.0" || first["id"].(float64) != 1 {
		t.Fatalf("unexpected initialize response: %v", first)
	}
	var second map[string]any
	if err := decoder.Decode(&second); err != nil {
		t.Fatal(err)
	}
	result := second["result"].(map[string]any)
	if len(result["tools"].([]any)) < 5 {
		t.Fatalf("expected tool definitions, got %v", result)
	}
	var third map[string]any
	if err := decoder.Decode(&third); err != nil {
		t.Fatal(err)
	}
	resources := third["result"].(map[string]any)["resources"].([]any)
	if len(resources) != 10 {
		t.Fatalf("expected ten resources, got %v", resources)
	}
}

func TestWorkspacePathRejectsTraversalAndExternalSymlink(t *testing.T) {
	server, err := NewServer(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := server.workspacePath("../outside.json", false); err == nil {
		t.Fatal("expected traversal path to be rejected")
	}
}

func TestValidateArtifactToolReturnsErrorResult(t *testing.T) {
	server, err := NewServer(".")
	if err != nil {
		t.Fatal(err)
	}
	request := `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"tradex_validate_artifact","arguments":{"path":"does-not-exist.json","kind":"scenario"}}}` + "\n"
	var output strings.Builder
	if err := server.Serve(context.Background(), strings.NewReader(request), &output); err != nil {
		t.Fatal(err)
	}
	var response map[string]any
	if err := json.Unmarshal([]byte(output.String()), &response); err != nil {
		t.Fatal(err)
	}
	toolResult := response["result"].(map[string]any)
	if toolResult["isError"] != true {
		t.Fatalf("expected tool error result, got %v", toolResult)
	}
}

func TestHermesSpecificationResourcesAreReadOnlyAndDiscoverable(t *testing.T) {
	specRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(specRoot, "docs", "08-ai"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(specRoot, "docs", "08-ai", "agent-boundaries.md"), []byte("# boundaries\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	server, err := NewServerWithSpecRoot(".", specRoot)
	if err != nil {
		t.Fatal(err)
	}
	resources := server.resources()
	found := false
	for _, resource := range resources {
		if resource["uri"] == "hermes://docs/08-ai/agent-boundaries.md" {
			found = true
		}
	}
	if !found {
		t.Fatalf("Hermes resource was not listed: %v", resources)
	}
	result, callErr := server.readResource(json.RawMessage(`{"uri":"hermes://docs/08-ai/agent-boundaries.md"}`))
	if callErr != nil {
		t.Fatal(callErr.message)
	}
	contents := result["contents"].([]map[string]any)
	if len(contents) != 1 || contents[0]["text"] != "# boundaries\n" {
		t.Fatalf("unexpected resource contents: %v", result)
	}
}
