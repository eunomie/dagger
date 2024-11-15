package main

import (
	"context"
	"fmt"
	"path/filepath"
	"ruby-sdk/internal/dagger"
)

const (
	RubyImage     = "ruby:3.3.6-alpine:3.20"
	RubyDigest    = "sha256:caeab43b356463e63f87af54a03de1ae4687b36da708e6d37025c557ade450f8"
	ModSourcePath = "/lib"
	GenPath       = "sdk"
)

type RubySdk struct {
	SourceDir     *dagger.Directory
	RequiredPaths []string
}

func New(
	// Directory with the ruby SDK source code.
	// +optional
	// +defaultPath="/sdk/ruby"
	// +ignore=["**", "!generated/", "!lib/", "!dagger.gemspec", "!Gemfile"]
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
	return dag.GeneratedCode(ctr.Directory(ModSourcePath)).
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
		From(fmt.Sprintf("%s@%s", RubyImage, RubyDigest))

	// Mounts Ruby SDK code and installs it
	// Runs codegen using the schema json provided by the dagger engine
	ctr := base.
		WithDirectory("/sdk", m.SourceDir).
		WithWorkdir("/sdk")

	sdkDir := ctr.
		WithMountedFile("/schema.json", introspectionJSON).
		WithExec([]string{
			"scripts/codegen.rb",
			"dagger:codegen",
			"--schema-file",
			"/schema.json",
		}).
		WithoutDirectory("vendor").
		WithoutDirectory("scripts").
		WithoutFile("Gemfile.lock").
		Directory(".")

	srcPath := filepath.Join(ModSourcePath, subPath)
	sdkPath := filepath.Join(srcPath, GenPath)
	runtime := dag.CurrentModule().Source()

	ctxDir := modSource.ContextDirectory().
		WithoutDirectory(filepath.Join(subPath, "vendor")).
		WithoutDirectory(filepath.Join(subPath, GenPath))

	ctr = ctr.
		WithMountedDirectory("/opt/template", runtime.Directory("template")).
		WithMountedFile("/init-template.sh", runtime.File("scripts/init-template.sh")).
		WithMountedDirectory(ModSourcePath, ctxDir).
		WithDirectory(sdkPath, sdkDir).
		WithWorkdir(srcPath).
		WithExec([]string{"/init-template.sh", name}).
		WithExec([]string{"bundle", "install"}).
		WithEntrypoint([]string{filepath.Join(srcPath, "entrypoint.rb")})

	return ctr, nil
}

func (m *RubySdk) ModuleRuntime(
	ctx context.Context,
	modSource *dagger.ModuleSource,
	introspectionJSON *dagger.File,
) (*dagger.Container, error) {
	return m.CodegenBase(ctx, modSource, introspectionJSON)
}
