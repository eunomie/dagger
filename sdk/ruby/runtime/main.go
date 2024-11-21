package main

import (
	"context"
	"fmt"
	"path/filepath"

	"ruby-sdk/internal/dagger"

	"github.com/iancoleman/strcase"
)

const (
	RubyImage        = "ruby:3.3.6-alpine3.20"
	RubyDigest       = "sha256:caeab43b356463e63f87af54a03de1ae4687b36da708e6d37025c557ade450f8"
	ModSourceDirPath = "/src"
	GenPath          = "lib/dagger"
	codegenBinPath   = "/codegen"
	schemaPath       = "/schema.json"
)

type RubySdk struct {
	SDKSourceDir  *dagger.Directory
	RequiredPaths []string
	moduleConfig  moduleConfig
}

type moduleConfig struct {
	name    string
	subPath string
}

func (c *moduleConfig) modulePath() string {
	return filepath.Join(ModSourceDirPath, c.subPath)
}

func (c *moduleConfig) sdkPath() string {
	return filepath.Join(c.modulePath(), GenPath)
}

func New(
	// Directory with the ruby SDK source code.
	// +optional
	sdkSourceDir *dagger.Directory,
) (*RubySdk, error) {
	if sdkSourceDir == nil {
		return nil, fmt.Errorf("sdk source directory not provided")
	}
	return &RubySdk{
		RequiredPaths: []string{},
		SDKSourceDir:  sdkSourceDir,
	}, nil
}

func (m *RubySdk) setModuleConfig(ctx context.Context, modSource *dagger.ModuleSource) error {
	name, err := modSource.ModuleOriginalName(ctx)
	if err != nil {
		return fmt.Errorf("could not load module name: %w", err)
	}

	subPath, err := modSource.SourceSubpath(ctx)
	if err != nil {
		return fmt.Errorf("could not load source subpath: %w", err)
	}

	m.moduleConfig = moduleConfig{
		name:    name,
		subPath: subPath,
	}

	return nil
}

func (m *RubySdk) Codegen(
	ctx context.Context,
	modSource *dagger.ModuleSource,
	introspectionJSON *dagger.File,
) (*dagger.GeneratedCode, error) {
	if err := m.setModuleConfig(ctx, modSource); err != nil {
		return nil, err
	}
	ctr, err := m.CodegenBase(ctx, modSource, introspectionJSON)
	if err != nil {
		return nil, err
	}
	codegen := dag.
		Directory().
		WithDirectory(
			"/",
			ctr.Directory(ModSourceDirPath))

	return dag.GeneratedCode(
		codegen,
	).
		WithVCSGeneratedPaths([]string{
			GenPath + "/**",
		}).
		WithVCSIgnoredPaths([]string{
			GenPath,
		}), nil
}

func (m *RubySdk) CodegenBase(
	_ context.Context,
	modSource *dagger.ModuleSource,
	introspectionJSON *dagger.File,
) (*dagger.Container, error) {
	base := m.base()

	base = base.
		WithMountedDirectory("/opt/module", dag.CurrentModule().Source().Directory(".")).
		WithDirectory(ModSourceDirPath, modSource.ContextDirectory()).
		With(m.generatedSDK(introspectionJSON)).
		WithWorkdir(m.moduleConfig.modulePath()).
		With(m.template)

	return base, nil
}

func (m *RubySdk) base() *dagger.Container {
	return dag.
		Container().
		From(fmt.Sprintf("%s@%s", RubyImage, RubyDigest)).
		WithExec([]string{"apk", "add", "git", "openssh", "curl"})
}

func (m *RubySdk) generateSDKDirectory(
	ctr *dagger.Container,
	introspectionJSON *dagger.File,
) *dagger.Directory {
	return m.SDKSourceDir.
		WithoutDirectory("codegen").
		WithoutDirectory("runtime").
		WithDirectory(".", m.generateClient(ctr, introspectionJSON))
}

func (m *RubySdk) generateClient(
	ctr *dagger.Container,
	introspectionJSON *dagger.File,
) *dagger.Directory {
	return ctr.
		// Add dagger codegen binary.
		WithMountedFile(codegenBinPath, m.SDKSourceDir.File("/codegen")).
		// Mount the introspection file.
		WithMountedFile(schemaPath, introspectionJSON).
		// Generate the ruby client from the introspection file.
		WithExec([]string{
			codegenBinPath,
			"--lang", "ruby",
			"--output", ModSourceDirPath,
			"--module-name", m.moduleConfig.name,
			"--module-context-path", m.moduleConfig.modulePath(),
			"--introspection-json-path", schemaPath,
		}, dagger.ContainerWithExecOpts{
			ExperimentalPrivilegedNesting: true,
		}).
		Directory(m.moduleConfig.sdkPath())
}

func (m *RubySdk) generatedSDK(
	introspectionJSON *dagger.File,
) dagger.WithContainerFunc {
	return func(ctr *dagger.Container) *dagger.Container {
		sdk := m.generateSDKDirectory(ctr, introspectionJSON)
		return ctr.
			WithDirectory(
				filepath.Join(m.moduleConfig.modulePath(), "sdk"),
				m.SDKSourceDir.Directory("lib/")).
			WithDirectory(filepath.Join(m.moduleConfig.modulePath(), "sdk/dagger"), sdk, dagger.ContainerWithDirectoryOpts{
				Include: []string{
					"client.gen.rb",
				},
			})
	}
}

func (m *RubySdk) template(
	ctr *dagger.Container,
) *dagger.Container {
	moduleName := m.moduleConfig.name
	camelModuleName := strcase.ToCamel(moduleName)
	snakeModuleName := strcase.ToSnake(moduleName)
	tmplModuleFileName := filepath.Join("lib", "module.rb")
	moduleFileName := filepath.Join("lib", snakeModuleName+".rb")
	return ctr.
		WithDirectory(filepath.Dir(moduleFileName), ctr.Directory("/opt/module/template"), dagger.ContainerWithDirectoryOpts{
			Include: []string{
				"module.rb",
			},
		}).
		WithExec([]string{"sed", "-i", "-e", fmt.Sprintf("s/DaggerModule/%s/g", camelModuleName), tmplModuleFileName}).
		WithExec([]string{"mv", tmplModuleFileName, moduleFileName}).
		WithDirectory(".", ctr.Directory("/opt/module/template"), dagger.ContainerWithDirectoryOpts{
			Include: []string{
				"Gemfile",
				"main.rb",
			},
		}).
		WithExec([]string{"sed", "-i", "-e", fmt.Sprintf("s/DaggerModule/%s/g", camelModuleName), "main.rb"}).
		WithExec([]string{"sed", "-i", "-e", fmt.Sprintf("s/dagger_module/%s/g", snakeModuleName), "main.rb"}).
		WithExec([]string{"bundle", "install"})
}

func (m *RubySdk) ModuleRuntime(
	ctx context.Context,
	modSource *dagger.ModuleSource,
	introspectionJSON *dagger.File,
) (*dagger.Container, error) {
	if err := m.setModuleConfig(ctx, modSource); err != nil {
		return nil, err
	}
	ctr, err := m.CodegenBase(ctx, modSource, introspectionJSON)
	if err != nil {
		return nil, err
	}

	moduleName := m.moduleConfig.name
	camelModuleName := strcase.ToCamel(moduleName)
	snakeModuleName := strcase.ToSnake(moduleName)
	entryPointFile := filepath.Join(m.moduleConfig.modulePath(), "entrypoint.rb")
	ctr = ctr.
		WithFile(
			entryPointFile,
			ctr.Directory("/opt/module/template").File("entrypoint.rb"),
		).
		WithExec([]string{"sed", "-i", "-e", fmt.Sprintf("s/DaggerModule/%s/g", camelModuleName), entryPointFile}).
		WithExec([]string{"sed", "-i", "-e", fmt.Sprintf("s/dagger_module/%s/g", snakeModuleName), entryPointFile}).
		WithWorkdir(m.moduleConfig.modulePath()).
		WithEntrypoint([]string{"ruby", entryPointFile})

	return ctr, nil
}
