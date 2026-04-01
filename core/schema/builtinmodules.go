package schema

import (
	"fmt"
	"os"
	"strings"

	"github.com/opencontainers/go-digest"

	"github.com/dagger/dagger/engine/distconsts"
)

// builtinModuleRef parses an "sdk:<lang>:<module>" ref string and returns
// the OCI manifest digest for the embedded module, plus the subpath within
// the tarball's rootfs where the module lives.
func builtinModuleRef(ref string) (manifestDigest digest.Digest, subpath string, err error) {
	rest := strings.TrimPrefix(ref, "sdk:")
	parts := strings.SplitN(rest, ":", 2)
	if len(parts) != 2 {
		return "", "", fmt.Errorf("invalid builtin module ref %q: expected sdk:<lang>:<module>", ref)
	}
	lang, module := parts[0], parts[1]

	key := lang + ":" + module
	entry, ok := builtinModuleRegistry[key]
	if !ok {
		return "", "", fmt.Errorf("unknown builtin module ref %q", ref)
	}

	dgstStr := os.Getenv(entry.envName)
	if dgstStr == "" {
		return "", "", fmt.Errorf("builtin module %q not embedded in engine (env %s not set)", ref, entry.envName)
	}

	dgst, err := digest.Parse(dgstStr)
	if err != nil {
		return "", "", fmt.Errorf("invalid digest for builtin module %q: %w", ref, err)
	}

	return dgst, entry.subpath, nil
}

type builtinModuleEntry struct {
	envName string
	subpath string
}

var builtinModuleRegistry = map[string]builtinModuleEntry{
	"compat:develop": {
		envName: distconsts.CompatSDKDevelopManifestDigestEnvName,
		subpath: "sdk/compat/develop",
	},
}
