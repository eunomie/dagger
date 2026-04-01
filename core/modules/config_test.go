package modules

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestModuleConfigRuntimeCodegen(t *testing.T) {
	// Explicit false
	jsonData := `{"name":"test","engineVersion":"v0.20.4","runtimeCodegen":false}`
	var cfg ModuleConfig
	err := json.Unmarshal([]byte(jsonData), &cfg)
	require.NoError(t, err)
	require.NotNil(t, cfg.RuntimeCodegen)
	require.False(t, *cfg.RuntimeCodegen)

	out, err := json.Marshal(cfg)
	require.NoError(t, err)
	require.Contains(t, string(out), `"runtimeCodegen":false`)

	// Absent (default = runtime codegen enabled)
	jsonData2 := `{"name":"test","engineVersion":"v0.20.4"}`
	var cfg2 ModuleConfig
	err = json.Unmarshal([]byte(jsonData2), &cfg2)
	require.NoError(t, err)
	require.Nil(t, cfg2.RuntimeCodegen)

	out2, err := json.Marshal(cfg2)
	require.NoError(t, err)
	require.NotContains(t, string(out2), "runtimeCodegen")
}
