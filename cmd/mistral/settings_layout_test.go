package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRegistrationJSONIncludesConfigPath(t *testing.T) {
	var document struct {
		Result struct {
			Metadata struct {
				Version      string `json:"Version"`
				ConfigFields []struct {
					Name        string `json:"Name"`
					Type        string `json:"Type"`
					Description string `json:"Description"`
				} `json:"ConfigFields"`
			} `json:"metadata"`
		} `json:"result"`
	}
	if err := json.Unmarshal(registrationJSON(), &document); err != nil {
		t.Fatal(err)
	}
	if document.Result.Metadata.Version != pluginVersion {
		t.Fatalf("registration version = %q, want %q", document.Result.Metadata.Version, pluginVersion)
	}
	for _, field := range document.Result.Metadata.ConfigFields {
		if field.Name != "config-path" {
			continue
		}
		if field.Type != "string" || field.Description == "" {
			t.Fatalf("config-path field = %#v", field)
		}
		return
	}
	t.Fatal("config-path is missing from ConfigFields")
}

func TestUnquoteDoubleQuotedEscapes(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{`"a\\b"`, `a\b`},
		{`"a\"b"`, `a"b`},
		{`"a\nb"`, "a\nb"},
		{`"a\rb"`, "a\rb"},
		{`"a\tb"`, "a\tb"},
		{`"a\qb"`, `a\qb`},
		{`"C:\\cpa\\config.yaml"`, `C:\cpa\config.yaml`},
		{`'a\nb'`, `a\nb`},
		{`a\qb`, `a\qb`},
	}
	for _, item := range cases {
		if got := unquote(item.in); got != item.want {
			t.Errorf("unquote(%s) = %q, want %q", item.in, got, item.want)
		}
	}
}

func TestConfigPathBackslashRoundTrip(t *testing.T) {
	original := settings{
		BaseURL:    "https://api.mistral.ai/v1",
		ConfigPath: `C:\cpa\config.yaml`,
		Credentials: []credential{
			{ID: "primary", Label: "Primary", APIKey: "mstrl_test", Priority: 10},
		},
		Models: []modelAlias{{Client: "m", Upstream: "upstream-m"}},
	}
	block := renderPluginBlock(original, 4)
	if !strings.Contains(block, `config-path: "C:\\cpa\\config.yaml"`) {
		t.Fatalf("block did not escape the path:\n%s", block)
	}
	got := parseSettings([]byte(block))
	if got.ConfigPath != original.ConfigPath {
		t.Fatalf("ConfigPath = %q, want %q", got.ConfigPath, original.ConfigPath)
	}
}

func TestReplacePluginBlockIndentation(t *testing.T) {
	// step is the column distance between plugins, configs and mistral.
	for _, step := range []int{2, 4, 8} {
		mistralIndent := 2 * step
		mistralKey := strings.Repeat(" ", mistralIndent) + "mistral:"
		child := strings.Repeat(" ", mistralIndent+2)
		item := strings.Repeat(" ", mistralIndent+4)
		field := strings.Repeat(" ", mistralIndent+6)

		config := "plugins:\n" +
			strings.Repeat(" ", step) + "configs:\n" +
			strings.Repeat(" ", 2*step) + "other:\n" +
			strings.Repeat(" ", 2*step+2) + "enabled: false\n" +
			mistralKey + "\n" +
			child + "enabled: true\n" +
			child + "base-url: \"https://old.example/v1\"\n" +
			"root-key: true\n"

		updated, err := replacePluginBlock(config, settings{
			BaseURL: "https://new.example/v1",
			Credentials: []credential{
				{ID: "primary", Label: "Primary", APIKey: "mstrl_test", Priority: 10},
			},
		})
		if err != nil {
			t.Fatalf("step %d: %v", step, err)
		}

		for _, want := range []string{
			mistralKey,
			child + "base-url: \"https://new.example/v1\"",
			child + "credentials:",
			item + "- id: \"primary\"",
			field + "api-key: \"mstrl_test\"",
			"root-key: true",
		} {
			if !strings.Contains(updated, want) {
				t.Fatalf("step %d: missing %q in:\n%s", step, want, updated)
			}
		}
		if strings.Contains(updated, "old.example") {
			t.Fatalf("step %d: old value survived:\n%s", step, updated)
		}

		got := parseSettings([]byte(updated))
		if got.BaseURL != "https://new.example/v1" {
			t.Fatalf("step %d: BaseURL = %q", step, got.BaseURL)
		}
		cred, ok := got.pick()
		if !ok || cred.APIKey != "mstrl_test" {
			t.Fatalf("step %d: credential = %#v ok=%v", step, cred, ok)
		}
	}
}

func TestReplacePluginBlockCommentsAndBOM(t *testing.T) {
	config := "\ufeffplugins:  # plugin registry\n" +
		"  configs:  # plugin configs\n" +
		"    mistral:  # our plugin\n" +
		"      enabled: true\n" +
		"      base-url: \"https://old.example/v1\"\n" +
		"  other:  # sibling\n" +
		"    enabled: false\n"
	updated, err := replacePluginBlock(config, settings{
		BaseURL: "https://new.example/v1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(updated, "\ufeffplugins:") {
		t.Fatalf("BOM was not preserved:\n%s", updated)
	}
	if !strings.Contains(updated, "      base-url: \"https://new.example/v1\"") {
		t.Fatalf("nested block was not replaced:\n%s", updated)
	}
	if !strings.Contains(updated, "  other:  # sibling\n    enabled: false") {
		t.Fatalf("sibling was not preserved:\n%s", updated)
	}
}

func TestReplacePluginBlockRejectsWrongParentChain(t *testing.T) {
	cases := map[string]string{
		"mistral below a sibling": "plugins:\n" +
			"  configs:\n" +
			"    other:\n" +
			"      mistral:\n" +
			"        enabled: true\n",
		"configs below a sibling": "plugins:\n" +
			"  other:\n" +
			"    configs:\n" +
			"      mistral:\n" +
			"        enabled: true\n",
	}
	for name, config := range cases {
		if _, err := replacePluginBlock(config, settings{}); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestReplacePluginBlockIgnoresSameNameOutsideChain(t *testing.T) {
	config := "mistral:\n" +
		"  enabled: false\n" +
		"plugins:\n" +
		"  configs:\n" +
		"    mistral:\n" +
		"      enabled: true\n" +
		"      base-url: \"https://old.example/v1\"\n"
	updated, err := replacePluginBlock(config, settings{BaseURL: "https://new.example/v1"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(updated, "mistral:\n  enabled: false\nplugins:") {
		t.Fatalf("top-level mistral was changed:\n%s", updated)
	}
	if !strings.Contains(updated, "    base-url: \"https://new.example/v1\"") {
		t.Fatalf("nested block was not replaced:\n%s", updated)
	}
}

func TestResolveConfigPathPriority(t *testing.T) {
	dir := t.TempDir()
	explicit := filepath.Join(dir, "explicit.yaml")
	fromEnv := filepath.Join(dir, "env.yaml")
	for _, path := range []string{explicit, fromEnv} {
		if err := os.WriteFile(path, []byte("plugins:\n  configs:\n    mistral:\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv(configPathEnv, fromEnv)

	got, err := resolveConfigPath(explicit)
	if err != nil || got != explicit {
		t.Fatalf("explicit path = %q err=%v, want %q", got, err, explicit)
	}
	got, err = resolveConfigPath("")
	if err != nil || got != fromEnv {
		t.Fatalf("env path = %q err=%v, want %q", got, err, fromEnv)
	}
}

func TestResolveConfigPathMissing(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.yaml")
	_, err := resolveConfigPath(missing)
	if err == nil || !strings.Contains(err.Error(), missing) {
		t.Fatalf("explicit missing err = %v", err)
	}

	t.Setenv(configPathEnv, missing)
	_, err = resolveConfigPath("")
	if err == nil || !strings.Contains(err.Error(), missing) {
		t.Fatalf("env missing err = %v", err)
	}
}

func TestPersistSettingsUsesExplicitPathOverEnv(t *testing.T) {
	dir := t.TempDir()
	explicit := filepath.Join(dir, "explicit.yaml")
	fromEnv := filepath.Join(dir, "env.yaml")
	envContent := "plugins:\n  configs:\n    mistral:\n      base-url: \"https://env.example/v1\"\n"
	for path, content := range map[string]string{
		explicit: "plugins:\n  configs:\n    mistral:\n      base-url: \"https://old.example/v1\"\n",
		fromEnv:  envContent,
	} {
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv(configPathEnv, fromEnv)

	next := settings{
		BaseURL:    "https://new.example/v1",
		ConfigPath: explicit,
		Credentials: []credential{
			{ID: "primary", Label: "Primary", APIKey: "mstrl_test", Priority: 10},
		},
	}
	if err := persistSettings(next); err != nil {
		t.Fatal(err)
	}

	explicitRaw, err := os.ReadFile(explicit)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(explicitRaw), "https://new.example/v1") || strings.Contains(string(explicitRaw), "old.example") {
		t.Fatalf("explicit file not updated:\n%s", explicitRaw)
	}
	envRaw, err := os.ReadFile(fromEnv)
	if err != nil {
		t.Fatal(err)
	}
	if string(envRaw) != envContent {
		t.Fatalf("env file was modified:\n%s", envRaw)
	}
}

func TestPersistSettingsMissingExplicitPath(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.yaml")
	err := persistSettings(settings{ConfigPath: missing})
	if err == nil || !strings.Contains(err.Error(), missing) {
		t.Fatalf("err = %v", err)
	}
	if _, statErr := os.Stat(missing); !os.IsNotExist(statErr) {
		t.Fatalf("persistSettings created %s", missing)
	}
}

func TestParseSettingsConfigPath(t *testing.T) {
	raw := `    mistral:
      config-path: "C:\\cpa\\config.yaml"
      base-url: "https://api.mistral.ai/v1"
`
	got := parseSettings([]byte(raw))
	if got.ConfigPath != `C:\cpa\config.yaml` {
		t.Fatalf("ConfigPath = %q", got.ConfigPath)
	}
}
