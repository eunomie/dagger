package main

import (
	"context"
	"fmt"
	"path/filepath"
	"ruby-sdk/internal/dagger"
)

const (
	RubyImage        = "ruby:3.3.6-alpine3.20"
	RubyDigest       = "sha256:caeab43b356463e63f87af54a03de1ae4687b36da708e6d37025c557ade450f8"
	ModSourceDirPath = "/src"
	GenPath          = "lib/dagger"
	codegenBinPath   = "/codegen"
)

type RubySdk struct {
	SourceDir     *dagger.Directory
	RequiredPaths []string
}

func New(
	// Directory with the ruby SDK source code.
	// +optional
	// +defaultPath="/sdk/ruby"
	sdkSourceDir *dagger.Directory,
) (*RubySdk, error) {
	if sdkSourceDir == nil {
		return nil, fmt.Errorf("sdk source directory not provided")
	}
	return &RubySdk{
		RequiredPaths: []string{},
		SourceDir:     sdkSourceDir,
	}, nil
}

func (m *RubySdk) Codegen(
	ctx context.Context,
	modSource *dagger.ModuleSource,
	introspectionJSON *dagger.File,
) (*dagger.GeneratedCode, error) {
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
		//ctr.Directory(ModSourcePath),
		codegen,
	).
		WithVCSGeneratedPaths([]string{
			GenPath + "/**",
			"entrypoint.rb",
		}).
		WithVCSIgnoredPaths([]string{
			GenPath,
			"vendor",
		}), nil
}

func (m *RubySdk) CodegenBase(
	ctx context.Context,
	modSource *dagger.ModuleSource,
	introspectionJSON *dagger.File,
) (*dagger.Container, error) {
	name, err := modSource.ModuleOriginalName(ctx)
	if err != nil {
		return nil, fmt.Errorf("could not load module name: %w", err)
	}

	subPath, err := modSource.SourceSubpath(ctx)
	if err != nil {
		return nil, fmt.Errorf("could not load source subpath: %w", err)
	}

	base := dag.Container().
		From(fmt.Sprintf("%s@%s", RubyImage, RubyDigest)).
		WithExec([]string{"apk", "add", "git", "openssh", "curl"})

	sdk := m.
		addSDK().
		WithDirectory(".", m.generateClient(base, introspectionJSON, name, subPath))

	// Mounts Ruby SDK code and installs it
	// Runs codegen using the schema json provided by the dagger engine
	base = base.
		WithMountedDirectory("/opt/module", dag.CurrentModule().Source().Directory(".")).
		WithDirectory(ModSourceDirPath,
			dag.Directory().WithDirectory("/", modSource.ContextDirectory(), dagger.DirectoryWithDirectoryOpts{
				Include: m.moduleConfigFiles(subPath),
			})).
		WithDirectory(filepath.Join(ModSourceDirPath, subPath, GenPath), sdk).
		WithWorkdir(filepath.Join(ModSourceDirPath, subPath))
	base = base.WithDirectory(".", base.Directory("/opt/module/template"))

	base = base.WithExec([]string{"bundle", "install"})

	base = base.
		WithDirectory(ModSourceDirPath,
			dag.Directory().WithDirectory("/", modSource.ContextDirectory(), dagger.DirectoryWithDirectoryOpts{
				Exclude: append(m.moduleConfigFiles(subPath), filepath.Join(subPath, "sdk")),
			}))
	//WithDirectory("/sdk", m.SourceDir).
	//WithWorkdir("/sdk")

	/*
		srcPath := filepath.Join(ModSourceDirPath, subPath)
		sdkPath := filepath.Join(srcPath, GenPath)
		runtime := dag.CurrentModule().Source()

		ctxDir := modSource.ContextDirectory().
			WithoutDirectory(filepath.Join(subPath, "vendor")).
			WithoutDirectory(filepath.Join(subPath, GenPath))

		base = base.
			WithMountedDirectory("/opt/template", runtime.Directory("template")).
			WithMountedFile("/init-template.sh", runtime.File("scripts/init-template.sh")).
			WithMountedDirectory(ModSourceDirPath, ctxDir).
			WithDirectory(sdkPath, sdk).
			WithWorkdir(srcPath).
			WithExec([]string{"/init-template.sh", name}).
			WithExec([]string{"bundle", "install"}).
			WithEntrypoint([]string{"bundle", "exec", "ruby", filepath.Join(srcPath, "main.rb")})
	*/
	return base, nil
}

func (m *RubySdk) moduleConfigFiles(path string) []string {
	modConfigFiles := []string{
		"Gemfile",
		"Gemfile.lock",
	}

	for i, file := range modConfigFiles {
		modConfigFiles[i] = filepath.Join(path, file)
	}

	return modConfigFiles
}

func (m *RubySdk) addSDK() *dagger.Directory {
	return m.SourceDir.
		WithoutDirectory("codegen").
		WithoutDirectory("runtime")
}

func (m *RubySdk) generateClient(
	ctr *dagger.Container,
	introspectionJSON *dagger.File,
	name, subPath string,
) *dagger.Directory {
	return ctr.
		WithMountedFile(codegenBinPath, m.SourceDir.File("/codegen")).
		WithMountedFile("/schema.json", introspectionJSON).
		WithExec([]string{
			codegenBinPath,
			"--lang", "ruby",
			"--output", ModSourceDirPath,
			"--module-name", name,
			"--module-context-path", subPath,
			"--introspection-json-path", "/schema.json",
		}, dagger.ContainerWithExecOpts{
			ExperimentalPrivilegedNesting: true,
		}).
		Directory(ModSourceDirPath)
}

func (m *RubySdk) ModuleRuntime(
	ctx context.Context,
	modSource *dagger.ModuleSource,
	introspectionJSON *dagger.File,
) (*dagger.Container, error) {
	return m.CodegenBase(ctx, modSource, introspectionJSON)
}
