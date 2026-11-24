package plugin

// PluginID is the stable identifier the host sees for this plugin.
const PluginID = "mistral"

const pluginVersion = "0.5.1"

// Version reports the plugin version published in release metadata.
func Version() string {
	return pluginVersion
}
