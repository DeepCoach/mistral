package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	defaultBaseURL    = "https://api.mistral.ai/v1"
	defaultConfigPath = "/CLIProxyAPI/config.yaml"
	configPathEnv     = "CPA_MISTRAL_CONFIG_PATH"
	pluginConfigKey   = "mistral"
	// defaultStreamIdleSeconds is how long a started stream may stay silent
	// before it is treated as stalled. It matches the upstream Vibe client's
	// 300s idle limit.
	defaultStreamIdleSeconds = 300
)

type settings struct {
	BaseURL             string
	ConsoleURL          string
	ConfigPath          string
	LogRetention        int
	StreamIdleSeconds   int
	Failover            bool
	IgnoreProxy         bool
	CooldownSeconds     int
	QuotaMonitoring     bool
	QuotaRefreshSeconds int
	Credentials         []credential
	Models              []modelAlias
}

type credential struct {
	ID       string
	Label    string
	APIKey   string
	Disabled bool
	Priority int
}

type modelAlias struct {
	Client    string
	Upstream  string
	Reasoning string
}

func (s settings) upstream(client string) (string, string) {
	for _, item := range s.Models {
		if item.Client == client {
			upstream := item.Upstream
			if upstream == "" {
				upstream = item.Client
			}
			return upstream, item.Reasoning
		}
	}
	return client, ""
}

func (s settings) clientNames() []string {
	names := make([]string, 0, len(s.Models))
	for _, item := range s.Models {
		if item.Client != "" {
			names = append(names, item.Client)
		}
	}
	return names
}

func (s settings) pick() (credential, bool) {
	items := s.candidates()
	if len(items) == 0 {
		return credential{}, false
	}
	return items[0], true
}

func (s settings) configured() bool {
	_, ok := s.pick()
	return ok && len(s.Models) > 0
}

func defaultSettings() settings {
	return settings{
		BaseURL:         defaultBaseURL,
		ConsoleURL:      defaultConsoleURL,
		LogRetention:    200,
		Failover:        true,
		CooldownSeconds: 60,
		// A silent stream is a real failure mode, but the default has to sit
		// above the longest legitimate silence. Mistral generates a tool call
		// whole before sending it, so minutes can pass with no output. The
		// upstream Vibe client allows 300s of idle for the same reason; below
		// that the guard would abort healthy generations.
		StreamIdleSeconds:   defaultStreamIdleSeconds,
		QuotaMonitoring:     true,
		QuotaRefreshSeconds: 60,
		Models: []modelAlias{
			{Client: "deepseek-v4.1-flash", Upstream: "deepseek-v4-1-flash-1017"},
			{Client: "glm-5.3", Upstream: "zai-glm-5-3"},
			{Client: "glm-5.3-flash", Upstream: "zai-glm-5-3-flash"},
			{Client: "mistral-vibe-cli-latest", Upstream: "mistral-vibe-cli-latest"},
			{Client: "mistral-vibe-cli-with-tools", Upstream: "mistral-vibe-cli-with-tools"},
			{Client: "mistral-vibe-cli-fast", Upstream: "mistral-vibe-cli-fast"},
		},
	}
}

func parseSettings(configYAML []byte) settings {
	current := defaultSettings()
	text := strings.TrimSpace(string(configYAML))
	if text == "" {
		return current
	}
	parsed := parseSettingsText(text)
	if parsed.BaseURL != "" {
		current.BaseURL = strings.TrimRight(parsed.BaseURL, "/")
	}
	if parsed.ConsoleURL != nil {
		current.ConsoleURL = strings.TrimRight(*parsed.ConsoleURL, "/")
	}
	if parsed.ConfigPath != "" {
		current.ConfigPath = parsed.ConfigPath
	}
	if parsed.LogRetention > 0 {
		current.LogRetention = parsed.LogRetention
	}
	// Only an explicit setting overrides the default. Reading the parsed zero
	// value as "disable" would silently switch the idle guard off for every
	// config written before the key existed.
	if parsed.StreamIdleSeconds != nil {
		current.StreamIdleSeconds = *parsed.StreamIdleSeconds
	}
	if parsed.Failover != nil {
		current.Failover = *parsed.Failover
	}
	if parsed.IgnoreProxy != nil {
		current.IgnoreProxy = *parsed.IgnoreProxy
	}
	if parsed.CooldownSeconds != nil {
		current.CooldownSeconds = clampInt(*parsed.CooldownSeconds, 0, 3600)
	}
	if parsed.QuotaMonitoring != nil {
		current.QuotaMonitoring = *parsed.QuotaMonitoring
	}
	if parsed.QuotaRefreshSeconds != nil {
		current.QuotaRefreshSeconds = clampInt(*parsed.QuotaRefreshSeconds, 10, 3600)
	}
	if len(parsed.Models) > 0 {
		current.Models = parsed.Models
	}
	if len(parsed.Credentials) > 0 {
		current.Credentials = parsed.Credentials
	} else if parsed.APIKey != "" {
		current.Credentials = []credential{{ID: "primary", Label: "主凭据", APIKey: parsed.APIKey, Priority: 10}}
	}
	return current
}

type parsedSettings struct {
	BaseURL             string
	ConsoleURL          *string
	ConfigPath          string
	APIKey              string
	LogRetention        int
	StreamIdleSeconds   *int
	Failover            *bool
	IgnoreProxy         *bool
	CooldownSeconds     *int
	QuotaMonitoring     *bool
	QuotaRefreshSeconds *int
	Credentials         []credential
	Models              []modelAlias
}

func parseSettingsText(text string) parsedSettings {
	var out parsedSettings
	section := ""
	var cred credential
	var model modelAlias
	inCred := false
	inModel := false
	flushCred := func() {
		if cred.ID != "" || cred.APIKey != "" {
			if cred.ID == "" {
				cred.ID = "key"
			}
			if cred.Label == "" {
				cred.Label = cred.ID
			}
			out.Credentials = append(out.Credentials, cred)
		}
		cred = credential{}
	}
	flushModel := func() {
		if model.Client != "" {
			if model.Upstream == "" {
				model.Upstream = model.Client
			}
			out.Models = append(out.Models, model)
		}
		model = modelAlias{}
	}
	for _, line := range strings.Split(text, "\n") {
		trim := strings.TrimSpace(line)
		if trim == "" || strings.HasPrefix(trim, "#") {
			continue
		}
		// Only column-zero lines are top-level keys. CPA re-serializes the
		// plugin block with yaml.v3 before handing it to plugin.register, so a
		// nested "api-key" under a credential arrives indented; treating it as
		// the legacy top-level api-key would flush the credential before its
		// key is read and silently drop the key.
		indent := len(line) - len(strings.TrimLeft(line, " \t"))
		if indent == 0 {
			if inCred {
				flushCred()
				inCred = false
			}
			if inModel && !strings.HasPrefix(trim, "-") && !strings.Contains(trim, "upstream-id") && !strings.Contains(trim, "reasoning") && !strings.HasPrefix(trim, "id:") {
				flushModel()
				inModel = false
			}
		}
		if trim == "credentials:" {
			section = "credentials"
			continue
		}
		if trim == "models:" {
			if inModel {
				flushModel()
			}
			section = "models"
			inModel = false
			continue
		}
		name, value, ok := splitYAML(trim)
		if !ok && strings.HasPrefix(trim, "-") {
			item := strings.Trim(strings.TrimSpace(strings.TrimPrefix(trim, "-")), `"'`)
			if section == "models" && !strings.Contains(item, ":") {
				if alias, good := parseModelAlias(item); good {
					out.Models = append(out.Models, alias)
				}
			}
			if section == "credentials" {
				flushCred()
				inCred = true
			}
			if section == "models" && strings.Contains(item, "id:") {
				flushModel()
				inModel = true
				_, value, _ = splitYAML(strings.TrimPrefix(item, "-"))
				model.Client = unquote(strings.TrimSpace(value))
			}
			continue
		}
		if !ok {
			continue
		}
		switch {
		case section == "credentials" && name == "id":
			if inCred && cred.ID != "" {
				flushCred()
			}
			inCred = true
			cred.ID = value
		case section == "credentials" && name == "label":
			cred.Label = value
		case section == "credentials" && (name == "api-key" || name == "api_key"):
			cred.APIKey = value
		case section == "credentials" && name == "disabled":
			cred.Disabled = value == "true"
		case section == "credentials" && name == "priority":
			fmt.Sscanf(value, "%d", &cred.Priority)
		case section == "models" && name == "id":
			if inModel && model.Client != "" {
				flushModel()
			}
			inModel = true
			model.Client = value
		case section == "models" && (name == "upstream-id" || name == "upstream_id"):
			model.Upstream = value
		case section == "models" && name == "reasoning":
			model.Reasoning = value
		case name == "base-url" || name == "base_url":
			out.BaseURL = value
		case name == "console-url" || name == "console_url":
			console := value
			out.ConsoleURL = &console
		case name == "config-path" || name == "config_path":
			out.ConfigPath = value
		case name == "api-key" || name == "api_key":
			out.APIKey = value
		case name == "log-retention":
			fmt.Sscanf(value, "%d", &out.LogRetention)
		case name == "stream-idle-seconds":
			idle := 0
			if _, scanErr := fmt.Sscanf(value, "%d", &idle); scanErr == nil {
				out.StreamIdleSeconds = &idle
			}
		case name == "failover":
			enabled := value == "true" || value == "1" || value == "yes" || value == "on"
			out.Failover = &enabled
		case name == "ignore-proxy" || name == "ignore_proxy":
			enabled := value == "true" || value == "1" || value == "yes" || value == "on"
			out.IgnoreProxy = &enabled
		case name == "cooldown-seconds":
			var seconds int
			fmt.Sscanf(value, "%d", &seconds)
			out.CooldownSeconds = &seconds
		case name == "quota-monitoring" || name == "quota_monitoring":
			enabled := value == "true" || value == "1" || value == "yes" || value == "on"
			out.QuotaMonitoring = &enabled
		case name == "quota-refresh-seconds" || name == "quota_refresh_seconds":
			var seconds int
			fmt.Sscanf(value, "%d", &seconds)
			out.QuotaRefreshSeconds = &seconds
		}
	}
	if inCred {
		flushCred()
	}
	if inModel {
		flushModel()
	}
	return out
}

func parseModelAlias(raw string) (modelAlias, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" || strings.ContainsAny(raw, " \t") {
		return modelAlias{}, false
	}
	client, upstream, ok := strings.Cut(raw, "=")
	client = strings.TrimSpace(client)
	upstream = strings.TrimSpace(upstream)
	if client == "" {
		return modelAlias{}, false
	}
	if !ok || upstream == "" {
		upstream = client
	}
	return modelAlias{Client: client, Upstream: upstream}, true
}

func splitYAML(line string) (string, string, bool) {
	name, value, ok := strings.Cut(line, ":")
	if !ok {
		return "", "", false
	}
	return strings.TrimSpace(strings.TrimPrefix(name, "-")), unquote(strings.TrimSpace(value)), true
}

func unquote(value string) string {
	if len(value) >= 2 {
		if value[0] == '"' && value[len(value)-1] == '"' {
			return unescapeDoubleQuoted(value[1 : len(value)-1])
		}
		if value[0] == '\'' && value[len(value)-1] == '\'' {
			return value[1 : len(value)-1]
		}
	}
	return value
}

// unescapeDoubleQuoted decodes the escape sequences quoteYAML emits. Anything
// else (a Windows path separator, for instance) stays as written, so a quoted
// path survives a write/read cycle.
func unescapeDoubleQuoted(value string) string {
	var builder strings.Builder
	builder.Grow(len(value))
	for i := 0; i < len(value); i++ {
		if value[i] != '\\' || i+1 >= len(value) {
			builder.WriteByte(value[i])
			continue
		}
		switch value[i+1] {
		case '\\':
			builder.WriteByte('\\')
		case '"':
			builder.WriteByte('"')
		case 'n':
			builder.WriteByte('\n')
		case 'r':
			builder.WriteByte('\r')
		case 't':
			builder.WriteByte('\t')
		default:
			builder.WriteByte('\\')
			builder.WriteByte(value[i+1])
		}
		i++
	}
	return builder.String()
}

// quoteYAML writes a double-quoted YAML scalar. Backslash is escaped first:
// replacing quotes alone left a trailing backslash, or an escaped quote, to
// consume the closing quote and produce a document the host cannot parse.
func quoteYAML(value string) string {
	var builder strings.Builder
	builder.Grow(len(value) + 2)
	builder.WriteByte('"')
	for _, r := range value {
		switch r {
		case '\\':
			builder.WriteString(`\\`)
		case '"':
			builder.WriteString(`\"`)
		case '\n':
			builder.WriteString(`\n`)
		case '\r':
			builder.WriteString(`\r`)
		case '\t':
			builder.WriteString(`\t`)
		default:
			builder.WriteRune(r)
		}
	}
	builder.WriteByte('"')
	return builder.String()
}

// renderPluginBlock writes the plugin block using the indentation of the key
// it replaces. Children sit two columns deeper, list items four and their
// fields six, so 2-, 4- and 8-space documents keep their own style.
func renderPluginBlock(s settings, indent int) string {
	keyIndent := strings.Repeat(" ", indent)
	childIndent := strings.Repeat(" ", indent+2)
	itemIndent := strings.Repeat(" ", indent+4)
	fieldIndent := strings.Repeat(" ", indent+6)
	var buf strings.Builder
	fmt.Fprintf(&buf, "%s%s:\n", keyIndent, pluginConfigKey)
	fmt.Fprintf(&buf, "%senabled: true\n", childIndent)
	fmt.Fprintf(&buf, "%spriority: 100\n", childIndent)
	fmt.Fprintf(&buf, "%sbase-url: %s\n", childIndent, quoteYAML(s.BaseURL))
	fmt.Fprintf(&buf, "%sconsole-url: %s\n", childIndent, quoteYAML(s.ConsoleURL))
	if s.ConfigPath != "" {
		fmt.Fprintf(&buf, "%sconfig-path: %s\n", childIndent, quoteYAML(s.ConfigPath))
	}
	fmt.Fprintf(&buf, "%slog-retention: %d\n", childIndent, s.LogRetention)
	fmt.Fprintf(&buf, "%sstream-idle-seconds: %d\n", childIndent, s.StreamIdleSeconds)
	fmt.Fprintf(&buf, "%sfailover: %t\n", childIndent, s.Failover)
	fmt.Fprintf(&buf, "%signore-proxy: %t\n", childIndent, s.IgnoreProxy)
	fmt.Fprintf(&buf, "%scooldown-seconds: %d\n", childIndent, s.CooldownSeconds)
	fmt.Fprintf(&buf, "%squota-monitoring: %t\n", childIndent, s.QuotaMonitoring)
	fmt.Fprintf(&buf, "%squota-refresh-seconds: %d\n", childIndent, s.QuotaRefreshSeconds)
	fmt.Fprintf(&buf, "%scredentials:\n", childIndent)
	for _, item := range s.Credentials {
		fmt.Fprintf(&buf, "%s- id: %s\n", itemIndent, quoteYAML(item.ID))
		fmt.Fprintf(&buf, "%slabel: %s\n", fieldIndent, quoteYAML(item.Label))
		fmt.Fprintf(&buf, "%sapi-key: %s\n", fieldIndent, quoteYAML(item.APIKey))
		fmt.Fprintf(&buf, "%sdisabled: %t\n", fieldIndent, item.Disabled)
		fmt.Fprintf(&buf, "%spriority: %d\n", fieldIndent, item.Priority)
	}
	fmt.Fprintf(&buf, "%smodels:\n", childIndent)
	for _, item := range s.Models {
		fmt.Fprintf(&buf, "%s- id: %s\n", itemIndent, quoteYAML(item.Client))
		fmt.Fprintf(&buf, "%supstream-id: %s\n", fieldIndent, quoteYAML(item.Upstream))
		fmt.Fprintf(&buf, "%sreasoning: %s\n", fieldIndent, quoteYAML(item.Reasoning))
	}
	return buf.String()
}

// replacePluginBlock finds plugins -> configs -> mistral by indentation, then
// rewrites just that block. A mistral key under another parent is not a match.
func replacePluginBlock(config string, s settings) (string, error) {
	lines := strings.Split(config, "\n")
	start, indent, err := locatePluginBlock(lines)
	if err != nil {
		return "", err
	}
	end := blockEnd(lines, start, indent)
	block := renderPluginBlock(s, indent)
	var buf strings.Builder
	buf.WriteString(strings.Join(lines[:start], "\n"))
	if start > 0 {
		buf.WriteByte('\n')
	}
	buf.WriteString(strings.TrimRight(block, "\n"))
	buf.WriteByte('\n')
	if end < len(lines) {
		buf.WriteString(strings.Join(lines[end:], "\n"))
		if !strings.HasSuffix(buf.String(), "\n") {
			buf.WriteByte('\n')
		}
	}
	return buf.String(), nil
}

func locatePluginBlock(lines []string) (int, int, error) {
	pluginsIndex := -1
	pluginsIndent := 0
	for i, line := range lines {
		if isBlankLine(line) || isCommentLine(line) {
			continue
		}
		if name, ok := yamlKeyName(line); ok && name == "plugins" {
			if indent := indentWidth(line); pluginsIndex < 0 || indent < pluginsIndent {
				pluginsIndex = i
				pluginsIndent = indent
			}
		}
	}
	if pluginsIndex < 0 {
		return 0, 0, fmt.Errorf("plugin config block %s was not found", pluginConfigKey)
	}
	pluginsEnd := blockEnd(lines, pluginsIndex, pluginsIndent)
	configsIndex, configsIndent, ok := directChildKey(lines, pluginsIndex+1, pluginsEnd, pluginsIndent, "configs")
	if !ok {
		return 0, 0, fmt.Errorf("plugin config block %s was not found", pluginConfigKey)
	}
	configsEnd := blockEnd(lines, configsIndex, configsIndent)
	mistralIndex, mistralIndent, ok := directChildKey(lines, configsIndex+1, configsEnd, configsIndent, pluginConfigKey)
	if !ok {
		return 0, 0, fmt.Errorf("plugin config block %s was not found", pluginConfigKey)
	}
	return mistralIndex, mistralIndent, nil
}

// directChildKey returns the first key at the block's immediate child indent.
// Deeper keys belong to a sibling's subtree and are ignored.
func directChildKey(lines []string, start, end, parentIndent int, key string) (int, int, bool) {
	childIndent := -1
	for i := start; i < end; i++ {
		line := lines[i]
		if isBlankLine(line) || isCommentLine(line) {
			continue
		}
		indent := indentWidth(line)
		if indent <= parentIndent {
			return 0, 0, false
		}
		if childIndent < 0 {
			childIndent = indent
		} else if indent < childIndent {
			return 0, 0, false
		}
		if indent != childIndent {
			continue
		}
		if name, ok := yamlKeyName(line); ok && name == key {
			return i, indent, true
		}
	}
	return 0, 0, false
}

// blockEnd returns the first non-empty line that is not deeper than the key.
// Indentation and boundaries are read from the original line; only key
// comparisons strip a BOM or a trailing comment.
func blockEnd(lines []string, keyIndex, keyIndent int) int {
	for i := keyIndex + 1; i < len(lines); i++ {
		if isBlankLine(lines[i]) {
			continue
		}
		if indentWidth(lines[i]) <= keyIndent {
			return i
		}
	}
	return len(lines)
}

func yamlKeyName(line string) (string, bool) {
	line = strings.TrimSpace(stripYAMLComment(strings.TrimPrefix(line, "\ufeff")))
	if line == "" || strings.HasPrefix(line, "#") {
		return "", false
	}
	name, _, ok := strings.Cut(line, ":")
	if !ok {
		return "", false
	}
	return strings.TrimSpace(name), true
}

// stripYAMLComment removes a trailing comment. A '#' inside a value (no
// whitespace before it) is kept.
func stripYAMLComment(line string) string {
	for i := 0; i < len(line); i++ {
		if line[i] != '#' {
			continue
		}
		if i == 0 || line[i-1] == ' ' || line[i-1] == '\t' {
			return line[:i]
		}
	}
	return line
}

func isCommentLine(line string) bool {
	return strings.HasPrefix(strings.TrimSpace(strings.TrimPrefix(line, "\ufeff")), "#")
}

func isBlankLine(line string) bool {
	return strings.TrimSpace(strings.TrimPrefix(line, "\ufeff")) == ""
}

func indentWidth(line string) int {
	width := 0
	for width < len(line) && (line[width] == ' ' || line[width] == '\t') {
		width++
	}
	return width
}

// resolveConfigPath picks the config file to read and write. An explicit
// config-path or CPA_MISTRAL_CONFIG_PATH pointing at a missing file is an
// error, not a cue to create one.
func resolveConfigPath(explicit string) (string, error) {
	explicit = strings.TrimSpace(explicit)
	if explicit != "" {
		if !isConfigFile(explicit) {
			return "", fmt.Errorf("config file %s does not exist", explicit)
		}
		return explicit, nil
	}
	if env := strings.TrimSpace(os.Getenv(configPathEnv)); env != "" {
		if !isConfigFile(env) {
			return "", fmt.Errorf("config file %s from %s does not exist", env, configPathEnv)
		}
		return env, nil
	}
	candidates := []string{defaultConfigPath}
	if executable, err := os.Executable(); err == nil {
		candidates = append(candidates, filepath.Join(filepath.Dir(executable), "config.yaml"))
	}
	if workingDir, err := os.Getwd(); err == nil {
		candidates = append(candidates, filepath.Join(workingDir, "config.yaml"))
	}
	for _, candidate := range candidates {
		if isConfigFile(candidate) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("config file not found; tried %s", strings.Join(candidates, ", "))
}

func isConfigFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func clampInt(value, low, high int) int {
	if value < low {
		return low
	}
	if value > high {
		return high
	}
	return value
}

func maskKey(key string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	if len(key) <= 4 {
		return "****"
	}
	return "****" + key[len(key)-4:]
}
