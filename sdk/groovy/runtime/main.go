// Runtime module for the Groovy SDK

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	_ "embed"

	"groovy-sdk/internal/dagger"

	"github.com/iancoleman/strcase"
)

const (
	ModSourceDirPath = "/src"
	ModDirPath       = "/opt/module"
	GenPath          = "/dagger-io"
	GroovySDKPath    = "/dagger-groovy-sdk"
)

type GroovySdk struct {
	SDKSourceDir     *dagger.Directory
	JavaSDKSourceDir *dagger.Directory
	moduleConfig     moduleConfig
	// If true, --debug flag will be added to gradle commands to enable full debug logging
	GradleDebugLogging bool
}

type moduleConfig struct {
	name    string
	subPath string
	dirPath string
}

func (c *moduleConfig) modulePath() string {
	return filepath.Join(ModSourceDirPath, c.subPath)
}

func New(
	// Directory with the Groovy SDK source code.
	// +defaultPath="/sdk/groovy"
	// +ignore=["**", "!dagger-groovy-sdk/", "!runtime/template/", "!runtime/images/"]
	sdkSourceDir *dagger.Directory,
	// Directory with the Java SDK source code.
	// +defaultPath="/sdk/java"
	// +ignore=["**", "!dagger-codegen-maven-plugin/", "!dagger-java-annotation-processor/", "!dagger-java-sdk/", "!dagger-java-samples/pom.xml", "!LICENSE", "!README.md", "!pom.xml", "**/src/test", "**/target"]
	javaSDKSourceDir *dagger.Directory,
) (*GroovySdk, error) {
	if sdkSourceDir == nil {
		return nil, fmt.Errorf("sdk source directory not provided")
	}
	if javaSDKSourceDir == nil {
		return nil, fmt.Errorf("java sdk source directory not provided")
	}
	return &GroovySdk{
		SDKSourceDir:     sdkSourceDir,
		JavaSDKSourceDir: javaSDKSourceDir,
	}, nil
}

func (m *GroovySdk) WithConfig(
	// +default=false
	gradleDebugLogging bool,
) *GroovySdk {
	m.GradleDebugLogging = gradleDebugLogging
	return m
}

func (m *GroovySdk) Codegen(
	ctx context.Context,
	modSource *dagger.ModuleSource,
	introspectionJSON *dagger.File,
) (*dagger.GeneratedCode, error) {
	if err := m.setModuleConfig(ctx, modSource); err != nil {
		return nil, err
	}
	ctr, err := m.codegenBase(ctx, modSource, introspectionJSON)
	if err != nil {
		return nil, err
	}

	generatedCode, err := m.generateCode(ctx, ctr, introspectionJSON)
	if err != nil {
		return nil, err
	}

	return dag.
		GeneratedCode(dag.Directory().WithDirectory("/", generatedCode)).
		WithVCSGeneratedPaths([]string{
			"build/generated/**",
		}).
		WithVCSIgnoredPaths([]string{
			"build",
			".gradle",
		}), nil
}

// codegenBase takes the user module code, adds the generated SDK dependencies
// if the user module code is empty, creates a default module content based on the template from the SDK
// The generated container will *not* contain the SDK source code, but only the packages built from the SDK
func (m *GroovySdk) codegenBase(
	ctx context.Context,
	modSource *dagger.ModuleSource,
	introspectionJSON *dagger.File,
) (*dagger.Container, error) {
	ctr, err := m.buildAllDependencies(ctx, introspectionJSON)
	if err != nil {
		return nil, err
	}
	ctr = ctr.
		// Copy the user module directory under /src
		WithDirectory(ModSourceDirPath, modSource.ContextDirectory()).
		// Set the working directory to the one containing the sources to build, not just the module root
		WithWorkdir(m.moduleConfig.modulePath())
	// Add a default template if there's no existing user code
	ctr, err = m.addTemplate(ctx, ctr)
	if err != nil {
		return nil, err
	}
	// Set the version of the Dagger dependencies
	version, err := m.getDaggerVersionForModule(ctx, introspectionJSON)
	if err != nil {
		return nil, err
	}
	ctr = ctr.WithEnvVariable("DAGGER_DEPS_VERSION", version)
	return ctr, nil
}

// buildAllDependencies builds the Java SDK with Maven and the Groovy SDK with Gradle,
// publishing both to the local Maven repository
func (m *GroovySdk) buildAllDependencies(
	ctx context.Context,
	introspectionJSON *dagger.File,
) (*dagger.Container, error) {
	// Build Java SDK and get the Maven local repo as a directory
	m2Repo, err := m.buildJavaDependencies(ctx, introspectionJSON)
	if err != nil {
		return nil, err
	}

	version, err := m.getDaggerVersionForModule(ctx, introspectionJSON)
	if err != nil {
		return nil, err
	}

	// Get a Gradle container
	gradleCtr, err := m.gradleContainer(ctx)
	if err != nil {
		return nil, err
	}

	// Import the Java SDK Maven artifacts into the Gradle container's local repo,
	// then build and publish the Groovy SDK alongside them
	gradleCtr = gradleCtr.
		WithDirectory("/root/.m2/repository", m2Repo).
		// Mount Groovy SDK source
		WithDirectory(GroovySDKPath, m.SDKSourceDir.Directory("dagger-groovy-sdk")).
		WithWorkdir(GroovySDKPath).
		// Build and publish Groovy SDK to local Maven repo
		WithExec(m.gradleCommand(
			"gradle",
			"publishToMavenLocal",
			fmt.Sprintf("-PdaggerDepsVersion=%s", version),
		))

	return gradleCtr, nil
}

// buildJavaDependencies builds the Java SDK modules with Maven and returns the
// Maven local repository contents as a Directory. Uses a cache volume for Maven
// downloads, then copies the repo to a regular path for extraction.
func (m *GroovySdk) buildJavaDependencies(
	ctx context.Context,
	introspectionJSON *dagger.File,
) (*dagger.Directory, error) {
	ctr, err := m.mvnContainer(ctx)
	if err != nil {
		return nil, err
	}
	version, err := m.getDaggerVersionForModule(ctx, introspectionJSON)
	if err != nil {
		return nil, err
	}
	built := ctr.
		// Cache maven dependencies for download speed
		WithMountedCache("/root/.m2", dag.CacheVolume("sdk-groovy-maven-m2"), dagger.ContainerWithMountedCacheOpts{Sharing: dagger.CacheSharingModeLocked}).
		// Mount the introspection JSON file used to generate the SDK
		WithMountedFile("/schema.json", introspectionJSON).
		// Copy the Java SDK source directory
		WithDirectory(GenPath, m.JavaSDKSourceDir).
		WithWorkdir(GenPath).
		// Set the version of the dependencies
		WithExec(m.mavenCommand(
			"mvn",
			"versions:set",
			"-DgenerateBackupPoms=false",
			fmt.Sprintf("-DnewVersion=%s", version),
		)).
		// Build and install the Java SDK modules
		WithExec(m.mavenCommand(
			"mvn",
			"--projects", "dagger-codegen-maven-plugin,dagger-java-annotation-processor,dagger-java-sdk", "--also-make",
			"clean", "install",
			"-DskipTests",
			"-Ddaggerengine.schema=/schema.json",
		)).
		// Copy the Maven repo out of the cache mount to a regular path
		WithExec([]string{"cp", "-r", "/root/.m2/repository", "/maven-repo"})

	return built.Directory("/maven-repo"), nil
}

// addTemplate creates all the necessary files to start a new Groovy module
func (m *GroovySdk) addTemplate(
	ctx context.Context,
	ctr *dagger.Container,
) (*dagger.Container, error) {
	name := m.moduleConfig.name
	pkgName := strings.ReplaceAll(strings.ReplaceAll(strings.ToLower(name), "-", ""), "_", "")
	kebabName := strcase.ToKebab(name)
	camelName := strcase.ToCamel(name)

	// Check if there's a build.gradle inside the module path. If a file exists, no need to add the templates
	if _, err := ctr.File(filepath.Join(m.moduleConfig.modulePath(), "build.gradle")).Name(ctx); err == nil {
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
	settingsGradle, err := m.replace(ctx, templateDir,
		"settings.gradle", changes...)
	if err != nil {
		return ctr, fmt.Errorf("could not add template: %w", err)
	}

	buildGradle, err := m.replace(ctx, templateDir,
		"build.gradle", changes...)
	if err != nil {
		return ctr, fmt.Errorf("could not add template: %w", err)
	}

	changes = append(changes, repl{"DaggerModule", camelName})
	daggerModuleGroovy, err := m.replace(ctx, templateDir,
		filepath.Join("src", "main", "groovy", "io", "dagger", "modules", "daggermodule", "DaggerModule.groovy"),
		changes...)
	if err != nil {
		return ctr, fmt.Errorf("could not add template: %w", err)
	}

	// Copy them to the container, renamed to match the dagger module name
	ctr = ctr.
		WithNewFile(absPath("settings.gradle"), settingsGradle).
		WithNewFile(absPath("build.gradle"), buildGradle).
		WithNewFile(absPath("src", "main", "groovy", "io", "dagger", "modules", pkgName, fmt.Sprintf("%s.groovy", camelName)), daggerModuleGroovy)

	return ctr, nil
}

// generateCode builds and returns the generated source code and Groovy/Java classes
func (m *GroovySdk) generateCode(
	ctx context.Context,
	ctr *dagger.Container,
	introspectionJSON *dagger.File,
) (*dagger.Directory, error) {
	version, err := m.getDaggerVersionForModule(ctx, introspectionJSON)
	if err != nil {
		return nil, err
	}
	generatedDir := filepath.Join(m.moduleConfig.modulePath(), "build", "generated", "sources", "dagger")

	// Compile Groovy sources to trigger the AST transform which generates Entrypoint.java.
	// We only need compileGroovy here — compileJava is not needed for codegen since we only
	// return the generated source files, not compiled classes.
	compiled := ctr.
		WithEnvVariable("_DAGGER_GROOVY_SDK_MODULE_NAME", m.moduleConfig.name).
		WithEnvVariable("_DAGGER_GROOVY_GENERATED_DIR", generatedDir).
		WithExec(m.gradleCommand(
			"gradle",
			"compileGroovy",
			fmt.Sprintf("-PdaggerDepsVersion=%s", version),
		))
	return dag.
		Directory().
		// copy all user files
		WithDirectory(
			m.moduleConfig.modulePath(),
			ctr.Directory(m.moduleConfig.modulePath())).
		// copy the generated sources
		WithDirectory(
			filepath.Join(m.moduleConfig.modulePath(), "build", "generated", "sources", "dagger"),
			compiled.Directory(filepath.Join(m.moduleConfig.modulePath(), "build", "generated", "sources", "dagger"))).
		Directory(ModSourceDirPath), nil
}

func (m *GroovySdk) ModuleRuntime(
	ctx context.Context,
	modSource *dagger.ModuleSource,
	introspectionJSON *dagger.File,
) (*dagger.Container, error) {
	if err := m.setModuleConfig(ctx, modSource); err != nil {
		return nil, err
	}

	// Get a container with the user module sources and the SDK packages built and installed
	ctr, err := m.codegenBase(ctx, modSource, introspectionJSON)
	if err != nil {
		return nil, err
	}
	// Build the executable jar
	jar, err := m.buildJar(ctx, ctr, introspectionJSON)
	if err != nil {
		return nil, err
	}

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

// buildJar builds and returns the shadow JAR from the user module
func (m *GroovySdk) buildJar(
	ctx context.Context,
	ctr *dagger.Container,
	introspectionJSON *dagger.File,
) (*dagger.File, error) {
	version, err := m.getDaggerVersionForModule(ctx, introspectionJSON)
	if err != nil {
		return nil, err
	}

	generatedDir := filepath.Join(m.moduleConfig.modulePath(), "build", "generated", "sources", "dagger")
	versionFlag := fmt.Sprintf("-PdaggerDepsVersion=%s", version)

	// Two-pass build:
	//   Pass 1: compileGroovy triggers the AST transform which generates Entrypoint.java
	//   Pass 2: --rerun-tasks forces compileGroovy to re-run; this time it sees the
	//           generated Entrypoint.java in its source set and compiles it via joint
	//           compilation alongside the user's Groovy classes. Then shadowJar packages all.
	built := ctr.
		WithEnvVariable("_DAGGER_GROOVY_SDK_MODULE_NAME", m.moduleConfig.name).
		WithEnvVariable("_DAGGER_GROOVY_GENERATED_DIR", generatedDir).
		WithExec(m.gradleCommand("gradle", "compileGroovy", versionFlag)).
		WithExec(m.gradleCommand("gradle", "shadowJar", "--rerun-tasks", versionFlag))

	// Find the shadow JAR - it's the only .jar in build/libs/
	// because we set archiveClassifier = '' in the template
	jarName, err := built.
		WithExec([]string{"sh", "-c", "ls " + filepath.Join(m.moduleConfig.modulePath(), "build", "libs", "*.jar") + " | head -1"}).
		Stdout(ctx)
	if err != nil {
		return nil, fmt.Errorf("could not find shadow JAR: %w", err)
	}
	jarName = strings.TrimSpace(jarName)
	if jarName == "" {
		return nil, fmt.Errorf("no JAR file found in build/libs/")
	}

	return built.File(jarName), nil
}

func (m *GroovySdk) gradleContainer(ctx context.Context) (*dagger.Container, error) {
	return disableSVEOnArm64(ctx, m.GradleImage())
}

func (m *GroovySdk) mvnContainer(ctx context.Context) (*dagger.Container, error) {
	return disableSVEOnArm64(ctx, m.MavenImage())
}

func (m *GroovySdk) jreContainer(ctx context.Context) (*dagger.Container, error) {
	return disableSVEOnArm64(ctx, m.JavaImage())
}

func disableSVEOnArm64(ctx context.Context, ctr *dagger.Container) (*dagger.Container, error) {
	if platform, err := ctr.Platform(ctx); err != nil {
		return nil, err
	} else if strings.Contains(string(platform), "arm64") {
		return ctr.WithEnvVariable("_JAVA_OPTIONS", "-XX:UseSVE=0"), nil
	}
	return ctr, nil
}

func (m *GroovySdk) setModuleConfig(ctx context.Context, modSource *dagger.ModuleSource) error {
	modName, err := modSource.ModuleName(ctx)
	if err != nil {
		return err
	}
	subPath, err := modSource.SourceSubpath(ctx)
	if err != nil {
		return err
	}
	var dirPath string
	if kind, err := modSource.Kind(ctx); err != nil {
		return err
	} else if kind == dagger.ModuleSourceKindLocal {
		dirPath, err = modSource.LocalContextDirectoryPath(ctx)
		if err != nil {
			return err
		}
	}
	m.moduleConfig = moduleConfig{
		name:    modName,
		subPath: subPath,
		dirPath: dirPath,
	}

	return nil
}

func (m *GroovySdk) getDaggerVersionForModule(ctx context.Context, introspectionJSON *dagger.File) (string, error) {
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

func (m *GroovySdk) replace(
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

func (m *GroovySdk) gradleCommand(args ...string) []string {
	args = append(args, "--no-daemon")
	if m.GradleDebugLogging {
		args = append(args, "--debug")
	}
	return args
}

func (m *GroovySdk) mavenCommand(args ...string) []string {
	args = append(args, "--threads", "1C", "--no-transfer-progress")
	return args
}

//go:embed images/gradle/Dockerfile
var gradleImage string

func (m *GroovySdk) GradleImage() *dagger.Container {
	return dag.Directory().WithNewFile("Dockerfile", gradleImage).DockerBuild()
}

// MavenImage returns a Maven container for building Java SDK dependencies
func (m *GroovySdk) MavenImage() *dagger.Container {
	return dag.Directory().WithNewFile("Dockerfile", "FROM maven:3.9.9-eclipse-temurin-21-alpine@sha256:4cbb8bf76c46b97e028998f2486ed014759a8e932480431039bdb93dffe6813e\n").DockerBuild()
}

//go:embed images/java/Dockerfile
var javaImage string

func (m *GroovySdk) JavaImage() *dagger.Container {
	return dag.Directory().WithNewFile("Dockerfile", javaImage).DockerBuild()
}
