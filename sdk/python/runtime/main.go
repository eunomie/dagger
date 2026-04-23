// Runtime module for the Python SDK

package main

import (
	"context"
	_ "embed"
	"fmt"
	"path"
	"strings"

	"python-sdk/internal/dagger"
)

const (
	ModSourceDirPath      = "/src"
	RuntimeExecutablePath = "/runtime"
	GenDir                = "sdk"
	SDKGenPath            = "src/dagger/client/gen.py"
	UserGenPath           = "src/dagger_gen.py"
	SchemaPath            = "/schema.json"
	VenvPath              = "/opt/venv"
	ProjectCfg            = "pyproject.toml"
	PipCompileLock        = "requirements.lock"
	UvLock                = "uv.lock"
	MainObjectName        = "Main"
)

// UserConfig is the custom user configuration that users can add to their pyproject.toml.
//
// For example:
// ```toml
// [tool.dagger]
// use-uv = false
// ```
type UserConfig struct {
	// BaseImage is the image reference to use for the base container.
	BaseImage string `toml:"base-image"`

	// UseUv is for choosing the faster uv tool instead of pip to install packages.
	UseUv bool `toml:"use-uv"`

	// UvVersion is the version of the uv tool to use.
	//
	// By default, it's pinned to a specific version in each dagger version.
	UvVersion string `toml:"uv-version"`
}

func New(
	// Directory with the Python SDK source code.
	// +defaultPath=".."
	// +ignore=["**", "!pyproject.toml", "!uv.lock", "!src/**/*.py", "!src/**/*.typed", "!codegen/pyproject.toml", "!codegen/**/*.py", "!LICENSE", "!README.md", "!dist/*"]
	sdkSourceDir *dagger.Directory,
) (*PythonSdk, error) {
	// Shouldn't happen due to defaultPath, but just in case.
	if sdkSourceDir == nil {
		return nil, fmt.Errorf("sdk source directory not provided")
	}
	d, err := NewDiscovery(UserConfig{
		UseUv: true,
	})
	if err != nil {
		return nil, err
	}
	return &PythonSdk{
		Discovery:    d,
		SdkSourceDir: sdkSourceDir,
		Container:    dag.Container(),
		// TODO: remove the following when we no longer vendor every time
		VendorPath: GenDir,
	}, nil
}

//go:embed template/pyproject.toml
var tplToml string

//go:embed template/__init__.py
var tplInit string

//go:embed template/main.py
var tplMain string

// Functions for building the runtime module for the Python SDK.
//
// The server interacts directly with the ModuleRuntime and Codegen functions.
// The others were built to be composable and chainable to facilitate the
// creation of extension modules (custom SDKs that depend on this one).
type PythonSdk struct {
	// Directory with the Python SDK source code
	SdkSourceDir *dagger.Directory

	// Resulting container after each composing step
	Container *dagger.Container

	// Whether the module runtime should run in debug mode.
	Debug bool

	// The original module's name
	ModName string

	// The normalized python distribution package name (in pyproject.toml)
	ProjectName string

	// The normalized python import package name (in the filesystem)
	PackageName string

	// The normalized main object name in Python
	MainObjectName string

	// The source needed to load and run a module
	ModSource *dagger.ModuleSource

	// ContextDir is a copy of the context directory from the module source
	//
	// We add files to this directory, always joining paths with the source's
	// subpath. We could use modSource.Directory("") for that if it was read-only,
	// but since we have to mount the context directory in the end, rather than
	// mounting the context dir and then mounting the forked source dir on top,
	// we fork the context dir instead so there's only one mount in the end.
	ContextDir *dagger.Directory

	// ContextDirPath is a unique host path for the module being loaded
	//
	// HACK: this property is computed as a unique value for a ModuleSource to
	// provide a unique path on the filesystem. This is because the uv cache
	// uses hashes of source paths - so we need to have something unique, or we
	// can get very real conflicts in the uv cache.
	ContextDirPath string

	// Relative path from the context directory to the source directory
	SubPath string

	// Relative path to vendor client library into
	VendorPath string

	// True if the module is new and we need to create files from the template
	//
	// It's assumed that this is the case if there's no pyproject.toml file.
	IsInit bool

	// Discovery holds the logic for getting more information from the target module.
	// +private
	Discovery *Discovery

	// SelfCallsEnabled is true when the module source has the
	// SELF_CALLS experimental feature turned on (i.e. the user ran
	// `dagger init --with-self-calls` or `dagger develop --with-self-calls`).
	// When true, WithSDK runs the three-phase analyze -> merge -> generate
	// pipeline so gen.py contains bindings for the module's declared types.
	// +private
	SelfCallsEnabled bool

	// LegacyCodegenAtRuntime is true when the module has NOT opted
	// into skip-codegen-at-runtime (i.e. the user has not set
	// codegen.legacyCodegenAtRuntime=false in dagger.json).
	// When false, Codegen short-circuits (trusting committed
	// generated files) and ModuleRuntime skips WithSDK's codegen
	// phases entirely.
	// +private
	LegacyCodegenAtRuntime bool
}

// Generated code for the Python module
func (m *PythonSdk) Codegen(
	ctx context.Context,
	modSource *dagger.ModuleSource,
	introspectionJSON *dagger.File,
) (*dagger.GeneratedCode, error) {
	m, err := m.Common(ctx, modSource, introspectionJSON)
	if err != nil {
		return nil, err
	}

	if !m.LegacyCodegenAtRuntime {
		// Opted-in path: the user committed sdk/** and _dagger_main.py.
		// Assert the committed files exist and return the source tree
		// as-is, short-circuiting the mutations the legacy branch
		// layers onto m.Container below.
		//
		// NOTE: Common() still runs WithSDK here, so this Codegen path
		// currently re-generates the files before asserting their
		// presence. The follow-up patch (skip WithSDK in ModuleRuntime)
		// drops that redundant codegen pass, at which point
		// requireGeneratedFiles becomes a genuine guard against stale
		// checkouts.
		if err := m.requireGeneratedFiles(ctx); err != nil {
			return nil, err
		}
		return dag.
			GeneratedCode(m.ContextDir.WithoutDirectory("sdk/runtime")).
			WithVCSGeneratedPaths(m.genPaths()).
			WithVCSIgnoredPaths(m.ignorePaths()), nil
	}

	return dag.
		GeneratedCode(
			m.Container.Directory(m.ContextDirPath).
				WithoutDirectory("sdk/runtime")).
		WithVCSGeneratedPaths(m.genPaths()).
		WithVCSIgnoredPaths(m.ignorePaths()), nil
}

// Container for executing the Python module runtime
func (m *PythonSdk) ModuleRuntime(
	ctx context.Context,
	modSource *dagger.ModuleSource,
	introspectionJSON *dagger.File,
) (*dagger.Container, error) {
	runtime, err := m.Common(ctx, modSource, introspectionJSON)
	if err != nil {
		return nil, err
	}
	runtime = runtime.WithInstall()
	ctr := runtime.Container
	if runtime.Debug {
		ctr = ctr.Terminal()
	}
	return ctr, nil
}

// Container for executing the Python module runtime
func (m *PythonSdk) ModuleTypesExp(
	ctx context.Context,
	modSource *dagger.ModuleSource,
	introspectionJSON *dagger.File,
	outputFilePath string,
) (*dagger.Container, error) {
	runtime, err := m.Common(ctx, modSource, introspectionJSON)
	if err != nil {
		return nil, err
	}
	runtime = runtime.WithInstall()
	ctr := runtime.Container
	return ctr.
			WithEnvVariable("DAGGER_MODULE_FILE", outputFilePath).
			WithEntrypoint([]string{RuntimeExecutablePath, "--register"}),
		nil
}

// Common steps for the ModuleRuntime and Codegen functions
func (m *PythonSdk) Common(
	ctx context.Context,
	modSource *dagger.ModuleSource,
	// +optional
	introspectionJSON *dagger.File,
) (*PythonSdk, error) {
	// The following functions were built to be composable in a granular way,
	// to allow a custom SDK to depend on this one and hook into before or
	// after major steps in the process. For example, you can get the base
	// container, add system packages, use the new one with `WithContainer`,
	// and then continue with the rest of the steps. Without this, you'd need
	// to copy the entire function and modify it.

	// NB: In extension modules, Load is chainable.
	_, err := m.Load(ctx, modSource)
	if err != nil {
		return nil, err
	}
	_, err = m.WithBase()
	if err != nil {
		return nil, err
	}
	// When the module has opted into skip-codegen-at-runtime, skip the
	// WithSDK codegen phases entirely. The user's committed sdk/** and
	// src/<pkg>/_dagger_main.py are the sole source of truth at runtime;
	// Codegen's short-circuit has already verified their presence.
	builder := m
	if m.LegacyCodegenAtRuntime {
		builder = builder.WithSDK(introspectionJSON)
	}
	return builder.
		WithTemplate().
		WithSource().
		WithUpdates(), nil
}

// Get all the needed information from the module's metadata and source files
func (m *PythonSdk) Load(ctx context.Context, modSource *dagger.ModuleSource) (*PythonSdk, error) {
	m.ModSource = modSource
	m.ContextDir = modSource.ContextDirectory()
	debug, err := modSource.SDK().Debug(ctx)
	if err != nil {
		return nil, fmt.Errorf("runtime module load: %w", err)
	}
	m.Debug = debug

	selfCalls, err := modSource.ExperimentalFeatureEnabled(
		ctx, dagger.ModuleSourceExperimentalFeatureSelfCalls,
	)
	if err != nil {
		return nil, fmt.Errorf("runtime module load: check self-calls feature: %w", err)
	}
	m.SelfCallsEnabled = selfCalls

	legacy, err := modSource.LegacyCodegenAtRuntime(ctx)
	if err != nil {
		return nil, fmt.Errorf("runtime module load: check legacy-codegen flag: %w", err)
	}
	m.LegacyCodegenAtRuntime = legacy

	if err := m.Discovery.Load(ctx, m); err != nil {
		return nil, fmt.Errorf("runtime module load: %w", err)
	}

	return m, nil
}

// Initialize the base Python container
//
// Workdir is set to the module's source directory.
func (m *PythonSdk) WithBase() (*PythonSdk, error) {
	baseAddr := m.getImage(BaseImageName).String()

	// NB: Adding env vars with container images that were pulled allows
	// modules to reuse them for performance benefits.
	m.Container = dag.Container().
		// Base Python
		From(baseAddr).
		// This var is informational only, in case it's useful in a module.
		WithEnvVariable("DAGGER_BASE_IMAGE", baseAddr).
		WithEnvVariable("PYTHONUNBUFFERED", "1").
		WithEnvVariable("PIP_DISABLE_PIP_VERSION_CHECK", "1").
		WithEnvVariable("PIP_ROOT_USER_ACTION", "ignore").
		// Uv
		With(m.uv()).
		WithEnvVariable("UV_SYSTEM_PYTHON", "1").
		WithEnvVariable("UV_LINK_MODE", "copy").
		WithEnvVariable("UV_NATIVE_TLS", "1").
		WithEnvVariable("UV_PROJECT_ENVIRONMENT", "/opt/venv")

	if !m.UseUv() {
		m.Container = m.Container.WithMountedCache("/root/.cache/pip", dag.CacheVolume("modpython-pip"))
	}
	if m.IndexURL() != "" {
		m.Container = m.Container.WithEnvVariable("UV_INDEX_URL", m.IndexURL())
	}
	if m.ExtraIndexURL() != "" {
		m.Container = m.Container.WithEnvVariable("UV_EXTRA_INDEX_URL", m.ExtraIndexURL())
	}

	return m, nil
}

func (m *PythonSdk) uv() dagger.WithContainerFunc {
	uvImage := m.getImage(UvImageName)

	return func(ctr *dagger.Container) *dagger.Container {
		var bins *dagger.Directory
		// Use bundled uv binaries if version wasn't overridden.
		if m.Discovery.SdkHasFile("dist/uv") && uvImage.Equal(m.Discovery.DefaultImages[UvImageName]) {
			bins = m.SdkSourceDir.Directory("dist")
		} else {
			bins = dag.Container().From(uvImage.String()).Rootfs()
		}

		return ctr.
			WithMountedFile("/usr/local/bin/uv", bins.File("uv")).
			WithMountedFile("/usr/local/bin/uvx", bins.File("uvx")).
			WithMountedCache("/root/.cache/uv", dag.CacheVolume("modpython-uv")).
			// These are informational only, to be leveraged by the target module if needed.
			WithEnvVariable("DAGGER_UV_IMAGE", uvImage.String()).
			WithEnvVariable("DAGGER_UV_VERSION", uvImage.Tag())
	}
}

// Add the template files to skaffold a new module
//
// The following files are added:
// - /runtime
// - <source>/pyproject.toml
// - <source>/src/<package_name>/__init__.py
// - <source>/src/<package_name>/main.py
func (m *PythonSdk) WithTemplate() *PythonSdk {
	m.Container = m.Container.
		WithFile(
			RuntimeExecutablePath,
			dag.CurrentModule().Source().File("template/runtime.py"),
			dagger.ContainerWithFileOpts{Permissions: 0o755},
		).
		WithEntrypoint([]string{RuntimeExecutablePath})

	d := m.Discovery

	// NB: We can't detect if it's a new module with `dagger develop --sdk`
	// if there's also a pyproject.toml file to customize the base container.
	//
	// The reason for adding sources only on new modules is because it's
	// been reported that it's surprising for users to delete the pyhton
	// file on the host and not fail on `dagger functions` and `dagger call`,
	// if we always recreate from the template. That's because only `init`
	// and `develop` export the generated files back to the host, potentially
	// creating a discrepancy.
	//
	// Throwing an error on missing files when not a new module is less
	// surprising, which is done during discovery.

	if m.IsInit {
		// On `dagger init --sdk`, one can first set a `pyproject.toml` to
		// change the base image, but if it's `dagger develop --sdk` the
		// existence of this file will set d.IsInit = true, thus skipping
		// this entire branch.
		if !d.HasFile(ProjectCfg) {
			projCfg := strings.ReplaceAll(tplToml, "main", m.ProjectName)
			m.AddNewFile(ProjectCfg, VendorConfig(projCfg, m.VendorPath))
		}
		if !d.HasFile("*.py") {
			m.AddNewFile(
				path.Join("src", m.PackageName, "__init__.py"),
				strings.ReplaceAll(tplInit, MainObjectName, m.MainObjectName),
			)
			m.AddNewFile(
				path.Join("src", m.PackageName, "main.py"),
				strings.ReplaceAll(tplMain, MainObjectName, m.MainObjectName),
			)
		}
	}

	return m
}

// Add the SDK package to the source directory
//
// This includes regenerating the client bindings for the current API schema
// (codegen).
func (m *PythonSdk) WithSDK(introspectionJSON *dagger.File) *PythonSdk {
	if m.VendorPath != "" {
		src := m.SdkSourceDir
		// If not vendoring we don't care to remove this
		if m.Discovery.SdkHasFile("dist/") {
			src = src.WithoutDirectory("dist")
		}
		m.AddDirectory(m.VendorPath, src)
	}

	// Allow empty introspection to facilitate debugging the container with a
	// `dagger call module-runtime terminal` command.
	if introspectionJSON != nil {
		genFile, entryFile := m.runCodegen(introspectionJSON)

		genPath := UserGenPath

		// For now, patch vendored client library with generated bindings.
		// TODO: Always generate outside library, even if vendored.
		if m.VendorPath != "" {
			genPath = path.Join(m.VendorPath, SDKGenPath)
		}
		m.AddFile(genPath, genFile)

		// User package entrypoint lives at src/<package>/_dagger_main.py
		// inside the user's module subtree.
		entryPath := path.Join(
			"src",
			m.PackageName,
			"_dagger_main.py",
		)
		m.AddFile(entryPath, entryFile)
	}

	return m
}

// runCodegen runs the Python codegen pipeline inside the container and
// returns (a) the generated client bindings at /gen.py and (b) the
// generated runtime entrypoint at /_dagger_main.py.
//
// Pipeline:
//
//	Phase 0:  analyzer emit --metadata-output /module-metadata.json
//	                        [--schematool-output /module-types.json  (self-calls)]
//	Phase 1:  merge-schema  (self-calls only)  -> /extended-schema.json
//	Phase 2:  codegen generate                 -> /gen.py
//	Phase 3:  analyzer entrypoint              -> /_dagger_main.py
func (m *PythonSdk) runCodegen(
	introspectionJSON *dagger.File,
) (*dagger.File, *dagger.File) {
	userSourceDir := path.Join(
		m.ContextDirPath, m.SubPath, "src", m.PackageName,
	)

	ctr := m.Container.
		WithMountedFile(SchemaPath, introspectionJSON).
		WithMountedDirectory(m.ContextDirPath, m.ContextDir).
		WithMountedDirectory("/sdk", m.SdkSourceDir).
		WithWorkdir("/sdk").
		WithMountedFile(
			"/usr/local/bin/merge-schema",
			m.SdkSourceDir.File("dist/merge-schema"),
		)

	// Phase 0: analyze — one AST walk, metadata always, schematool only
	// on the self-calls path.
	phase0 := []string{
		"uv", "run", "--isolated", "--frozen",
		"python", "-m", "dagger.mod._analyzer", "emit",
		"--module-source-dir", userSourceDir,
		"--main-object", m.MainObjectName,
		"--module-name", m.ModName,
		"--metadata-output", "/module-metadata.json",
	}
	if m.SelfCallsEnabled {
		phase0 = append(phase0,
			"--introspection-json", SchemaPath,
			"--schematool-output", "/module-types.json",
		)
	}
	ctr = ctr.WithExec(phase0)

	// Phase 1 (conditional): merge
	schemaForCodegen := SchemaPath
	if m.SelfCallsEnabled {
		ctr = ctr.WithExec([]string{
			"merge-schema", "merge-schema",
			"--introspection-json-path", SchemaPath,
			"--module-types-path", "/module-types.json",
			"--output-path", "/extended-schema.json",
		})
		schemaForCodegen = "/extended-schema.json"
	}

	// Phase 2: client codegen (reuse the spec-1 shiv-or-uv-run logic).
	var codegenCmd []string
	if m.Discovery.SdkHasFile("dist/codegen") {
		ctr = ctr.
			WithMountedCache("/root/.shiv", dag.CacheVolume("shiv")).
			WithMountedFile(
				"/usr/local/bin/codegen",
				m.SdkSourceDir.File("dist/codegen"),
			)
		codegenCmd = []string{"codegen"}
	} else {
		codegenCmd = []string{
			"uv", "run", "--isolated", "--frozen", "--package", "codegen",
			"python", "-m", "codegen",
		}
	}
	ctr = ctr.WithExec(append(codegenCmd,
		"generate", "-i", schemaForCodegen, "-o", "/gen.py",
	))

	// Phase 3: entrypoint codegen — always runs.
	ctr = ctr.WithExec([]string{
		"uv", "run", "--isolated", "--frozen",
		"python", "-m", "dagger.mod._analyzer", "entrypoint",
		"--metadata", "/module-metadata.json",
		"--package", m.PackageName,
		"--output", "/_dagger_main.py",
	})

	return ctr.File("/gen.py"), ctr.File("/_dagger_main.py")
}

// requireGeneratedFiles ensures the module's committed generated
// files are present when legacyCodegenAtRuntime is off. Returns an
// actionable error if any required path is missing.
func (m *PythonSdk) requireGeneratedFiles(ctx context.Context) error {
	required := []string{
		// Full vendored SDK tree (covers sdk/src/dagger/client/gen.py
		// plus all other vendored files).
		path.Join(m.SubPath, m.VendorPath),
		// Generated runtime entrypoint (from spec 2).
		path.Join(m.SubPath, "src", m.PackageName, "_dagger_main.py"),
	}
	for _, rel := range required {
		exists, err := m.ContextDir.Exists(ctx, rel)
		if err != nil {
			return fmt.Errorf("check generated path %q: %w", rel, err)
		}
		if !exists {
			return fmt.Errorf(
				"module %q has codegen.legacyCodegenAtRuntime=false "+
					"but required generated path %q is missing. "+
					"Run `dagger develop` to regenerate.",
				m.ModName, rel)
		}
	}
	return nil
}

// genPaths returns the list of VCS-tracked (generated) paths for
// this module. Shared between the legacy Codegen path and the
// opted-in short-circuit.
func (m *PythonSdk) genPaths() []string {
	if m.VendorPath != "" {
		return []string{m.VendorPath + "/**"}
	}
	return []string{}
}

// ignorePaths returns the list of VCS-ignored paths for this module.
// Shared between the legacy Codegen path and the opted-in
// short-circuit.
func (m *PythonSdk) ignorePaths() []string {
	ignore := []string{".venv", "**/__pycache__"}
	if m.VendorPath != "" {
		ignore = append(ignore, m.VendorPath)
	}
	return ignore
}

// Add the module's source code
func (m *PythonSdk) WithSource() *PythonSdk {
	m.Container = m.Container.
		WithWorkdir(path.Join(m.ContextDirPath, m.SubPath)).
		WithMountedDirectory(m.ContextDirPath, m.ContextDir).
		// These are added as late as possible  to avoid cache invalidation
		// between different modules. It may be used by the runtime entrypoint
		// so only needed in ModuleRuntime but added here so that extension
		// modules get it for free since they need to reimplement ModuleRuntime.
		// It's ok since the previous layer is already dependent on the target
		// module's sources.
		WithEnvVariable("DAGGER_MODULE", m.ModName).
		WithEnvVariable("DAGGER_DEFAULT_PYTHON_PACKAGE", m.PackageName).
		WithEnvVariable("DAGGER_MAIN_OBJECT", m.MainObjectName)
	return m
}

// Make any updates to current source
func (m *PythonSdk) WithUpdates() *PythonSdk {
	if !m.UseUv() {
		return m
	}

	ctr := m.Container
	d := m.Discovery

	// Update lock file but without upgrading dependencies.
	switch {
	case m.UseUvLock():
		// Support uv.lock. Takes precedence.
		// Always update if uv.lock exists, but only create a new uv.lock
		// if init and there's not already a requirements.lock.
		ctr = ctr.WithExec([]string{"uv", "lock"})

	case d.HasFile(PipCompileLock) && !m.IsInit:
		// Support requirements.lock (legacy).
		args := []string{
			"uv", "pip", "compile", "-q", "--universal",
			"-o", PipCompileLock,
			ProjectCfg,
		}

		if m.VendorPath != "" {
			args = append(args, path.Join(m.VendorPath, ProjectCfg))
		}

		ctr = ctr.WithExec(args)
	}

	m.Container = ctr

	return m
}

// Install the module's package and dependencies
func (m *PythonSdk) WithInstall() *PythonSdk {
	// NB: Only enable bytecode compilation in `dagger call`
	// (not `dagger init/develop`), to avoid having to remove the .pyc files
	// before exporting the module back to the host.
	ctr := m.Container.WithEnvVariable("UV_COMPILE_BYTECODE", "1")

	// Support uv.lock for simple and fast project management workflow.
	if m.UseUvLock() {
		// While best practice is to sync dependencies first with only pyproject.toml and
		// uv.lock, user projects can have more required files for a minimally successful
		// `uv sync --no-install-project --no-dev`.
		// Besides, uv is fast enough that's not too bad to skip this optimization.
		m.Container = ctr.
			WithExec([]string{"uv", "sync", "--no-dev"}).
			// Activate virtualenv to avoid having to prepend `uv run` to the entrypoint.
			WithEnvVariable("VIRTUAL_ENV", "$UV_PROJECT_ENVIRONMENT", dagger.ContainerWithEnvVariableOpts{
				Expand: true,
			}).
			WithEnvVariable("PATH", "$VIRTUAL_ENV/bin:$PATH", dagger.ContainerWithEnvVariableOpts{
				Expand: true,
			})
		return m
	}

	// Fallback to pip-compile workflow (legacy).
	install := []string{"pip", "install", "-e", "./sdk", "-e", "."}
	check := []string{"pip", "check"}

	// uv has a compatible API with pip
	if m.UseUv() {
		// Support requirements.lock.
		if m.Discovery.HasFile(PipCompileLock) {
			// If there's a lock file, we assume that all the dependencies are
			// included in it so we can avoid resolving for them to get a faster
			// install.
			install = append(install, "--no-deps", "-r", PipCompileLock)
		}
		// pip compiles by default, but not uv
		install = append([]string{"uv"}, install...)
		check = append([]string{"uv"}, check...)
	}

	m.Container = ctr.
		WithExec(install).
		WithExec(check)

	return m
}
