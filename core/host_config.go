package core

// WellKnownHostConfigs maps well-known config names to paths relative
// to the user's home directory. SDK modules can request these files
// via ModuleSource.hostConfigFile(name).
var WellKnownHostConfigs = map[string]string{
	"maven-settings":    ".m2/settings.xml",
	"gradle-properties": ".gradle/gradle.properties",
	"pip-config":        ".config/pip/pip.conf",
	"npmrc":             ".npmrc",
}
