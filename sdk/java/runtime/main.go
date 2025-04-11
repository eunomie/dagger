// Runtime module for the Java SDK

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"java-sdk/internal/dagger"

	"github.com/iancoleman/strcase"
)

const (
	MavenImage  = "maven:3.9.9-eclipse-temurin-21-alpine"
	MavenDigest = "sha256:4cbb8bf76c46b97e028998f2486ed014759a8e932480431039bdb93dffe6813e"
	JavaImage   = "eclipse-temurin:21-jre-alpine-3.21"
	JavaDigest  = "sha256:4e9ab608d97796571b1d5bbcd1c9f430a89a5f03fe5aa6c093888ceb6756c502"

	ModSourceDirPath = "/src"
	ModDirPath       = "/opt/module"
	GenPath          = "/dagger-io"
)

type JavaSdk struct {
	SDKSourceDir *dagger.Directory
	moduleConfig moduleConfig
}

type moduleConfig struct {
	name    string
	subPath string
}

func (c *moduleConfig) modulePath() string {
	return filepath.Join(ModSourceDirPath, c.subPath)
}

func New(
	// Directory with the Java SDK source code.
	// dagger-java-samples is not necessary to build, but as it's referenced in the root pom.xml maven
	// will check if it's there. So we keep the pom.xml to fake it.
	// +defaultPath="/sdk/java"
	// +ignore=["**", "!dagger-codegen-maven-plugin/", "!dagger-java-annotation-processor/", "!dagger-java-sdk/", "!dagger-java-samples/pom.xml", "!LICENSE", "!README.md", "!pom.xml", "**/src/test", "**/target"]
	sdkSourceDir *dagger.Directory,
) (*JavaSdk, error) {
	if sdkSourceDir == nil {
		return nil, fmt.Errorf("sdk source directory not provided")
	}
	return &JavaSdk{
		SDKSourceDir: sdkSourceDir,
	}, nil
}

func (m *JavaSdk) Codegen(
	ctx context.Context,
	modSource *dagger.ModuleSource,
	introspectionJSON *dagger.File,
) (*dagger.GeneratedCode, error) {
	if err := m.setModuleConfig(ctx, modSource); err != nil {
		return nil, err
	}

	// Get a container with the user module sources and all dependencies, including the SDK packages built and installed
	moduleCtr, err := m.javaModuleCtr(ctx, modSource, introspectionJSON)
	if err != nil {
		return nil, err
	}

	generatedCode := dag.
		Directory().
		// copy all user files
		WithDirectory(
			m.moduleConfig.modulePath(),
			moduleCtr.Directory(m.moduleConfig.modulePath())).
		// copy all the generated code under target/generated-sources
		WithDirectory(
			filepath.Join(m.moduleConfig.modulePath(), "target", "generated-sources"),
			dag.Directory().
				// copy the generated entrypoint under target/generated-sources/entrypoint
				WithDirectory("annotations", moduleCtr.Directory(filepath.Join(m.moduleConfig.modulePath(), "target", "generated-sources", "annotations")))).
		Directory(ModSourceDirPath)

	return dag.
		GeneratedCode(dag.Directory().WithDirectory("/", generatedCode)).
		WithVCSGeneratedPaths([]string{
			"target/generated-sources/**",
		}).
		WithVCSIgnoredPaths([]string{
			"target",
		}), nil
}

func (m *JavaSdk) javaModuleCtr(
	ctx context.Context,
	modSource *dagger.ModuleSource,
	introspectionJSON *dagger.File,
) (*dagger.Container, error) {
	// Get the dagger version
	version, err := m.getDaggerVersionForModule(ctx, introspectionJSON)
	if err != nil {
		return nil, err
	}

	// We need a maven container
	ctr, err := m.mvnContainer(ctx)
	if err != nil {
		return nil, err
	}

	// Create a container to generate the SDK files based on the introspection file
	// order of operations is set to improve cachine
	deps := ctr.
		// Copy the SDK source directory to build the java dependencies
		WithDirectory(GenPath, m.SDKSourceDir).
		WithWorkdir(GenPath).
		// Set the version of the dependencies we are building to the version of the introspection file
		With(m.mvnExec("versions:set", "-DgenerateBackupPoms=false", fmt.Sprintf("-DnewVersion=%s", version))).
		// Build first the codegen plugin. This is the one that should only depend on the SDK source code/version but not on
		// introspection code
		With(m.mvnExec("install", "-pl", "dagger-codegen-maven-plugin")).
		// Mount the introspection file. This file might depend on the dependencies so it's good to not mount it before
		// that point we really need it
		WithMountedFile("/schema.json", introspectionJSON).
		// Build dagger-java-sdk (all the types required to create module source code)
		// and dagger-java-annotation-processor (to generate the entrypoint)
		// The generated files available contains non module related files, so better to remove them first
		// to ensure code generation is clean
		WithoutDirectory(filepath.Join(GenPath, "dagger-java-sdk", "src", "gen")).
		With(m.mvnExec(
			"--projects", "dagger-java-annotation-processor,dagger-java-sdk", "--also-make",
			"install",
			"-DskipTests",
			"-Ddaggerengine.schema=/schema.json",
		))

	// Create a container to deal with the user's module source code
	mod := ctr.
		// Copy the generated jars from above
		WithMountedDirectory("/root/.m2", deps.Directory("/root/.m2")).
		// Mount the user's module directory
		WithMountedDirectory(ModSourceDirPath, modSource.ContextDirectory()).
		// Set the working directory to the one containing the sources to build, not just the module root
		WithWorkdir(m.moduleConfig.modulePath())

	// Add a default template if there's no existing user code
	mod, err = m.addTemplate(ctx, mod)
	if err != nil {
		return nil, err
	}

	mod = mod.
		// Set the version of the Dagger dependencies to the version of the introspection file
		// This version is the one used to generate the dependencies above
		With(m.mvnExec(
			"versions:set-property",
			"-DgenerateBackupPoms=false",
			"-Dproperty=dagger.module.deps",
			fmt.Sprintf("-DnewVersion=%s", version),
		))

	mod = mod.
		// Compile. This will be used to get the dependencies and to package
		With(m.mvnExec("compile"))

	return mod, nil
}

// addTemplate creates all the necessary files to start a new Java module
func (m *JavaSdk) addTemplate(
	ctx context.Context,
	ctr *dagger.Container,
) (*dagger.Container, error) {
	name := m.moduleConfig.name
	pkgName := strings.ReplaceAll(strings.ReplaceAll(strings.ToLower(name), "-", ""), "_", "")
	kebabName := strcase.ToKebab(name)
	camelName := strcase.ToCamel(name)

	// Check if there's a pom.xml inside the module path. If a file exist, no need to add the templates
	if _, err := ctr.File(filepath.Join(m.moduleConfig.modulePath(), "pom.xml")).Name(ctx); err == nil {
		return ctr, nil
	}

	absPath := func(rel ...string) string {
		return filepath.Join(append([]string{m.moduleConfig.modulePath()}, rel...)...)
	}

	changes := []repl{
		{"dagger-module-placeholder", kebabName},
		{"daggermoduleplaceholder", pkgName},
	}

	// Edit template content so that they match the dagger module name
	templateDir := dag.CurrentModule().Source().Directory("template")
	pomXML, err := m.replace(ctx, templateDir,
		"pom.xml", changes...)
	if err != nil {
		return ctr, fmt.Errorf("could not add template: %w", err)
	}

	changes = append(changes, repl{"DaggerModule", camelName})
	daggerModuleJava, err := m.replace(ctx, templateDir,
		filepath.Join("src", "main", "java", "io", "dagger", "modules", "daggermodule", "DaggerModule.java"),
		changes...)
	if err != nil {
		return ctr, fmt.Errorf("could not add template: %w", err)
	}
	packageInfoJava, err := m.replace(ctx, templateDir,
		filepath.Join("src", "main", "java", "io", "dagger", "modules", "daggermodule", "package-info.java"),
		changes...)
	if err != nil {
		return ctr, fmt.Errorf("could not add template: %w", err)
	}

	// And copy them to the container, renamed to match the dagger module name
	ctr = ctr.
		WithNewFile(absPath("pom.xml"), pomXML).
		WithNewFile(absPath("src", "main", "java", "io", "dagger", "modules", pkgName, fmt.Sprintf("%s.java", camelName)), daggerModuleJava).
		WithNewFile(absPath("src", "main", "java", "io", "dagger", "modules", pkgName, "package-info.java"), packageInfoJava)

	return ctr, nil
}

func (m *JavaSdk) ModuleRuntime(
	ctx context.Context,
	modSource *dagger.ModuleSource,
	introspectionJSON *dagger.File,
) (*dagger.Container, error) {
	if err := m.setModuleConfig(ctx, modSource); err != nil {
		return nil, err
	}

	// Get a container with the user module sources and all dependencies, including the SDK packages built and installed
	moduleCtr, err := m.javaModuleCtr(ctx, modSource, introspectionJSON)
	if err != nil {
		return nil, err
	}

	// Build the executable jar
	jar, err := m.buildJar(ctx, moduleCtr)
	if err != nil {
		return nil, err
	}

	// Create the runnable container
	javaCtr, err := m.jreContainer(ctx)
	if err != nil {
		return nil, err
	}
	javaCtr = javaCtr.
		WithFile(filepath.Join(ModDirPath, "module.jar"), jar).
		WithWorkdir(ModDirPath).
		WithEntrypoint([]string{"java", "-jar", filepath.Join(ModDirPath, "module.jar")})

	return javaCtr, nil
}

// buildJar builds and returns the generated jar from the user module
func (m *JavaSdk) buildJar(
	ctx context.Context,
	ctr *dagger.Container,
) (*dagger.File, error) {
	return m.finalJar(ctx,
		ctr.
			// set the module name as an environment variable so we ensure constructor is only on main object
			WithEnvVariable("_DAGGER_JAVA_SDK_MODULE_NAME", m.moduleConfig.name).
			// build the final jar
			With(m.mvnExec("clean", "package", "-DskipTests")))
}

func (m *JavaSdk) mvnExec(args ...string) dagger.WithContainerFunc {
	return func(ctr *dagger.Container) *dagger.Container {
		cmd := []string{"mvn"}
		cmd = append(cmd, args...)
		// cmd = append(cmd, "-e") // this is just for debug purpose, uncommit it needed
		return ctr.
			WithExec(cmd)
	}
}

// finalJar will return the jar corresponding to the user module built
// In order to have the final container as minimal as possible, we just want to be able to run a jar.
// To make it easy, we will rename the jar as module.jar
// But that's not easy, as we don't know the name of the built jar, so we will ask maven to give us the
// artifactId and the version so we can get the jar name
func (m *JavaSdk) finalJar(
	ctx context.Context,
	ctr *dagger.Container,
) (*dagger.File, error) {
	artifactID, err := ctr.
		WithExec([]string{"mvn", "org.apache.maven.plugins:maven-help-plugin:evaluate", "-Dexpression=project.artifactId", "-q", "-DforceStdout"}).
		Stdout(ctx)
	if err != nil {
		return nil, err
	}
	version, err := ctr.
		WithExec([]string{"mvn", "org.apache.maven.plugins:maven-help-plugin:evaluate", "-Dexpression=project.version", "-q", "-DforceStdout"}).
		Stdout(ctx)
	if err != nil {
		return nil, err
	}
	jarFileName := fmt.Sprintf("%s-%s.jar", artifactID, version)

	return ctr.File(filepath.Join(m.moduleConfig.modulePath(), "target", jarFileName)), nil
}

func (m *JavaSdk) mvnContainer(ctx context.Context) (*dagger.Container, error) {
	ctr := dag.
		Container().
		From(fmt.Sprintf("%s@%s", MavenImage, MavenDigest))
	return disableSVEOnArm64(ctx, ctr)
}

func (m *JavaSdk) jreContainer(ctx context.Context) (*dagger.Container, error) {
	ctr := dag.
		Container().
		From(fmt.Sprintf("%s@%s", JavaImage, JavaDigest))
	return disableSVEOnArm64(ctx, ctr)
}

func disableSVEOnArm64(ctx context.Context, ctr *dagger.Container) (*dagger.Container, error) {
	if platform, err := ctr.Platform(ctx); err != nil {
		return nil, err
	} else if strings.Contains(string(platform), "arm64") {
		return ctr.WithEnvVariable("_JAVA_OPTIONS", "-XX:UseSVE=0"), nil
	}
	return ctr, nil
}

func (m *JavaSdk) setModuleConfig(ctx context.Context, modSource *dagger.ModuleSource) error {
	modName, err := modSource.ModuleName(ctx)
	if err != nil {
		return err
	}
	subPath, err := modSource.SourceSubpath(ctx)
	if err != nil {
		return err
	}
	m.moduleConfig = moduleConfig{
		name:    modName,
		subPath: subPath,
	}

	return nil
}

func (m *JavaSdk) getDaggerVersionForModule(ctx context.Context, introspectionJSON *dagger.File) (string, error) {
	content, err := introspectionJSON.Contents(ctx)
	if err != nil {
		return "", err
	}
	var introspectJSON IntrospectJSON
	if err = json.Unmarshal([]byte(content), &introspectJSON); err != nil {
		return "", err
	}
	return fmt.Sprintf(
		"%s-%s-module",
		strings.TrimPrefix(introspectJSON.SchemaVersion, "v"),
		m.moduleConfig.name,
	), nil
}

type IntrospectJSON struct {
	SchemaVersion string `json:"__schemaVersion"`
}

type repl struct {
	oldString string
	newString string
}

func (m *JavaSdk) replace(
	ctx context.Context,
	dir *dagger.Directory,
	path string,
	changes ...repl,
) (string, error) {
	content, err := dir.File(path).Contents(ctx)
	if err != nil {
		return "", err
	}
	for _, change := range changes {
		content = strings.ReplaceAll(content, change.oldString, change.newString)
	}
	return content, nil
}
