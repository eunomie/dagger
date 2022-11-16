package main

import (
	"context"
	"fmt"
	"os"

	"dagger.io/dagger"
)

func main() {
	// Create dagger client
	ctx := context.Background()
	client, err := dagger.Connect(ctx, dagger.WithLogOutput(os.Stdout))
	if err != nil {
		panic(err)
	}
	defer client.Close()

	project := client.Host().Workdir()

	// Build our app
	builder := client.Container().
		From("golang:latest").
		WithMountedDirectory("/src", project).
		WithWorkdir("/src").
		WithEnvVariable("CGO_ENABLED", "0").
		Exec(dagger.ContainerExecOpts{
			Args: []string{"go", "build", "-o", "myapp"},
		})

	// highlight-start
	// Publish binary on Alpine base
	prodImage := client.Container().
		From("alpine")
	prodImage = prodImage.WithFS(
		prodImage.FS().WithFile("/bin/myapp",
			builder.File("/src/myapp"),
		)).
		WithEntrypoint([]string{"/bin/myapp"})
	// highlight-end

	addr, err := prodImage.Publish(ctx, "localhost:5000/multistage")
	if err != nil {
		panic(err)
	}

	fmt.Println(addr)
}
