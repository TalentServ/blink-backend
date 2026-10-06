package setupproj

import "testing"

func TestHostedPayloadMapsWizardContext(t *testing.T) {
	out := HostedPayload("Fitoyo App", "", "42", "Food delivery", "# req\n", map[string]any{
		"stakeholderAssignments": []any{
			map[string]any{"roleId": "product_owner", "personName": "Ada", "personEmail": "ada@ex.com"},
		},
		"repositoryModel": "polyrepo",
		"topology":        "web + api",
		"topologyConfirmation": map[string]any{
			"structure":    "monorepo",
			"confirmedBy":  "Ada:ada@ex.com",
			"evidenceRef":  "blink:project:42",
		},
		"repositories": []any{
			map[string]any{"name": "fitoyo-api", "purpose": "backend"},
		},
		"integrations": []any{
			map[string]any{"provider": "github", "organization": "acme"},
			map[string]any{"provider": "jira", "projectKey": "FIT"},
		},
	})
	if out["projectId"] != "fitoyo-app" {
		t.Fatalf("projectId=%v", out["projectId"])
	}
	stakes, _ := out["stakeholders"].(map[string]string)
	if stakes["product_owner"] != "Ada <ada@ex.com>" {
		t.Fatalf("stakeholders=%v", stakes)
	}
	if _, ok := out["blinkContext"]; ok {
		t.Fatal("must not nest blinkContext")
	}
	confirmation, _ := out["topologyConfirmation"].(map[string]any)
	if confirmation["structure"] != "monorepo" || confirmation["confirmedBy"] != "Ada:ada@ex.com" {
		t.Fatalf("topologyConfirmation=%v", confirmation)
	}
	handles, _ := out["integration_handles"].([]map[string]any)
	if len(handles) != 2 {
		t.Fatalf("handles=%v", handles)
	}
}

func TestHasRequired(t *testing.T) {
	files := map[string]string{}
	for _, p := range RequiredFiles {
		files[p] = "ok\n"
	}
	if !HasRequired(files) {
		t.Fatal("expected required set to pass")
	}
	delete(files, RequiredFiles[0])
	if HasRequired(files) {
		t.Fatal("missing file must fail")
	}
}

func TestParseManifestAcceptsFilesKey(t *testing.T) {
	body := []byte(`{
		"status": "ok",
		"files": [
			{"path": ".cursor/ai-sdlc/setup/project-initialisation-input.yaml", "content": "a\n"},
			{"path": ".cursor/ai-sdlc/setup/setup-result.yaml", "content": "b\n"},
			{"path": ".cursor/ai-sdlc/setup/setup-response-contract.yaml", "content": "c\n"},
			{"path": ".cursor/ai-sdlc/governance/role-registry.yaml", "content": "d\n"},
			{"path": ".cursor/ai-sdlc/greenfield-project-spec.yaml", "content": "e\n"},
			{"path": ".cursor/ai-sdlc/intake/requirements/requirement-source-history.yaml", "content": "f\n"}
		],
		"contextReady": true,
		"nextAction": {"user_command": "/configure-stakeholders"}
	}`)
	res, err := parseManifest(body)
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "overlay_ready" || !HasRequired(res.Overlay) {
		t.Fatalf("got %+v", res)
	}
}
