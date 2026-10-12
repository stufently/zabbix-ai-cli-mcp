package mcp_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The desktop extension is the one installation path that is read-only unless
// the user turns writes on. That default lives in a JSON file no Go code
// reads, so a flipped default or a hard-coded environment value would ship
// without anything else noticing.
func TestDesktopExtensionIsReadOnlyByDefault(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "mcpb", "manifest.json"))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	var m struct {
		Server struct {
			MCPConfig struct {
				Args []string          `json:"args"`
				Env  map[string]string `json:"env"`
			} `json:"mcp_config"`
		} `json:"server"`
		UserConfig map[string]struct {
			Type    string `json:"type"`
			Default *bool  `json:"default"`
		} `json:"user_config"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("manifest is not JSON: %v", err)
	}

	setting, ok := m.UserConfig["allow_write"]
	if !ok {
		t.Fatal("manifest has no allow_write setting; the user could not opt in to writes")
	}
	if setting.Type != "boolean" {
		t.Errorf("allow_write type = %q, want boolean", setting.Type)
	}
	if setting.Default == nil || *setting.Default {
		t.Error("allow_write must default to false: writes are an explicit opt-in for the extension")
	}

	// The env value must come from the user's choice. A literal "true" here
	// would turn writes on whatever the setting says.
	if got := m.Server.MCPConfig.Env["ZABBIX_AI_CLI_MCP_ALLOW_WRITE"]; got != "${user_config.allow_write}" {
		t.Errorf("ZABBIX_AI_CLI_MCP_ALLOW_WRITE = %q, want it taken from user_config.allow_write", got)
	}
	if len(m.Server.MCPConfig.Args) != 1 || m.Server.MCPConfig.Args[0] != "mcp" {
		t.Errorf("server args = %q, want exactly [mcp]", m.Server.MCPConfig.Args)
	}
}
