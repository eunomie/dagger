package core

import (
	"context"
	"path/filepath"
	"testing"

	"dagger.io/dagger"
	"github.com/stretchr/testify/require"

	"github.com/dagger/testctx"
)

type GroovySuite struct{}

func TestGroovy(t *testing.T) {
	testctx.New(t, Middleware()...).RunTests(GroovySuite{})
}

func (GroovySuite) TestInit(_ context.Context, t *testctx.T) {
	t.Run("from alias", func(ctx context.Context, t *testctx.T) {
		c := connect(ctx, t)

		modGen := c.Container().From(golangImage).
			WithMountedFile(testCLIBinPath, daggerCliFile(t, c)).
			WithWorkdir("/work").
			With(daggerExec("init", "--name=bare", "--sdk=groovy"))

		out, err := modGen.
			With(daggerQuery(`{containerEcho(stringArg:"hello"){stdout}}`)).
			Stdout(ctx)
		require.NoError(t, err)
		require.JSONEq(t, `{"containerEcho":{"stdout":"hello\n"}}`, out)
	})

	t.Run("from upstream", func(ctx context.Context, t *testctx.T) {
		c := connect(ctx, t)

		modGen := c.Container().From(golangImage).
			WithMountedFile(testCLIBinPath, daggerCliFile(t, c)).
			WithWorkdir("/work").
			With(daggerExec("init", "--name=bare", "--sdk=github.com/dagger/dagger/sdk/groovy"))

		out, err := modGen.
			With(daggerQuery(`{containerEcho(stringArg:"hello"){stdout}}`)).
			Stdout(ctx)
		require.NoError(t, err)
		require.JSONEq(t, `{"containerEcho":{"stdout":"hello\n"}}`, out)
	})

	t.Run("from alias with ref", func(ctx context.Context, t *testctx.T) {
		c := connect(ctx, t)

		modGen := c.Container().From(golangImage).
			WithMountedFile(testCLIBinPath, daggerCliFile(t, c)).
			WithWorkdir("/work").
			With(daggerExec("init", "--name=bare", "--sdk=groovy@main"))

		out, err := modGen.
			With(daggerQuery(`{containerEcho(stringArg:"hello"){stdout}}`)).
			Stdout(ctx)
		require.NoError(t, err)
		require.JSONEq(t, `{"containerEcho":{"stdout":"hello\n"}}`, out)
	})

	t.Run("grep-dir", func(ctx context.Context, t *testctx.T) {
		c := connect(ctx, t)

		modGen := c.Container().From(golangImage).
			WithMountedFile(testCLIBinPath, daggerCliFile(t, c)).
			WithWorkdir("/work").
			With(daggerExec("init", "--name=bare", "--sdk=groovy"))

		out, err := modGen.
			With(daggerCall("grep-dir", "--directory-arg=.", "--pattern=dagger")).
			Stdout(ctx)
		require.NoError(t, err)
		require.Contains(t, out, "dagger")
	})
}

func groovyModule(t *testctx.T, c *dagger.Client, moduleName string) *dagger.Container {
	t.Helper()
	modSrc, err := filepath.Abs(filepath.Join("./testdata/modules/groovy", moduleName))
	require.NoError(t, err)

	groovySdkSrc, err := filepath.Abs("../../sdk/groovy")
	require.NoError(t, err)

	javaSdkSrc, err := filepath.Abs("../../sdk/java")
	require.NoError(t, err)

	return goGitBase(t, c).
		WithDirectory("modules/"+moduleName, c.Host().Directory(modSrc)).
		WithDirectory("sdk/groovy", c.Host().Directory(groovySdkSrc)).
		WithDirectory("sdk/java", c.Host().Directory(javaSdkSrc)).
		WithWorkdir("/work/modules/" + moduleName)
}
