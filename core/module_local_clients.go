package core

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"

	"github.com/dagger/dagger/core/workspace"
	"github.com/dagger/dagger/dagql"
)

// localClients are the local clients declared for a module by the config that
// sits with its files: the workspace's config for a module loaded from the
// current workspace, and the config in its own tree for a module loaded from
// git or from a directory.
type localClients struct {
	modulePath string
	declared   func(path string) []string
	load       func(ctx context.Context, path string) (dagql.ObjectResult[*ModuleSource], error)
}

// ModuleLocalClients returns the local clients declared for the module loaded
// from src, each as a module source over the files the engine loaded for that
// client. A source built this way holds no workspace, so it cannot read back
// into the caller's workspace or the module's.
func ModuleLocalClients(ctx context.Context, src *ModuleSource) (dagql.ObjectResultArray[*ModuleSource], error) {
	clients, err := moduleLocalClients(ctx, src)
	if err != nil || clients == nil {
		return nil, err
	}
	var sources dagql.ObjectResultArray[*ModuleSource]
	for _, path := range clients.declared(clients.modulePath) {
		client, err := clients.load(ctx, path)
		if err != nil {
			return nil, fmt.Errorf("load local client %q: %w", path, err)
		}
		sources = append(sources, client)
	}
	return sources, nil
}

func moduleLocalClients(ctx context.Context, src *ModuleSource) (*localClients, error) {
	switch src.Kind {
	case ModuleSourceKindLocal:
		if src.Local == nil {
			return nil, nil
		}
		return workspaceLocalClients(ctx, src)
	case ModuleSourceKindGit:
		if src.Git == nil || src.Git.UnfilteredContextDir.Self() == nil {
			return nil, nil
		}
		return treeLocalClients(ctx, src.Git.UnfilteredContextDir, src.SourceRootSubpath)
	case ModuleSourceKindDir:
		if src.DirSrc == nil || src.DirSrc.OriginalContextDir.Self() == nil {
			return nil, nil
		}
		return treeLocalClients(ctx, src.DirSrc.OriginalContextDir, src.SourceRootSubpath)
	default:
		return nil, nil
	}
}

func workspaceLocalClients(ctx context.Context, src *ModuleSource) (*localClients, error) {
	var ws *Workspace
	if bound, ok := WorkspaceFromContext(ctx); ok {
		ws = bound.Self()
	} else {
		query, err := CurrentQuery(ctx)
		if err != nil {
			return nil, err
		}
		ws, err = query.Server.CurrentWorkspace(ctx)
		if errors.Is(err, ErrNoCurrentWorkspace) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
	}
	if ws.HostPath() == "" {
		return nil, nil
	}
	modulePath, err := filepath.Rel(ws.HostPath(), filepath.Join(src.Local.ContextDirectoryPath, src.SourceRootSubpath))
	if err != nil {
		return nil, err
	}
	modulePath = filepath.ToSlash(modulePath)
	if len(ws.ModuleClients(modulePath)) == 0 {
		return nil, nil
	}

	dag, err := CurrentDagqlServer(ctx)
	if err != nil {
		return nil, err
	}
	var wsRes dagql.ObjectResult[*Workspace]
	if err := dag.Select(ctx, dag.Root(), &wsRes, dagql.Selector{Field: "currentWorkspace"}); err != nil {
		return nil, err
	}
	merge := func(ctx context.Context, into, dir dagql.ObjectResult[*Directory]) (dagql.ObjectResult[*Directory], error) {
		dirID, err := dir.ID()
		if err != nil {
			return into, err
		}
		err = dag.Select(ctx, into, &into, dagql.Selector{
			Field: "withDirectory",
			Args: []dagql.NamedInput{
				{Name: "path", Value: dagql.String(".")},
				{Name: "source", Value: dagql.NewID[*Directory](dirID)},
			},
		})
		return into, err
	}
	var loadedFiles func(ctx context.Context, path string) (dagql.ObjectResult[*Directory], string, error)
	loadedFiles = func(ctx context.Context, path string) (dagql.ObjectResult[*Directory], string, error) {
		var dir dagql.ObjectResult[*Directory]
		var loaded dagql.ObjectResult[*ModuleSource]
		if err := dag.Select(ctx, wsRes, &loaded, dagql.Selector{
			Field: "moduleSource",
			Args:  []dagql.NamedInput{{Name: "path", Value: dagql.String("/" + path)}},
		}); err != nil {
			return dir, "", err
		}
		dir = loaded.Self().ContextDirectory
		// A local dependency resolves inside the tree its dependent is served
		// from, so its files come along.
		for _, dep := range loaded.Self().Dependencies {
			if dep.Self() == nil || dep.Self().Kind != ModuleSourceKindLocal {
				continue
			}
			depDir, _, err := loadedFiles(ctx, filepath.ToSlash(filepath.Clean(dep.Self().SourceRootSubpath)))
			if err != nil {
				return dir, "", fmt.Errorf("load local dependency %q: %w", dep.Self().SourceRootSubpath, err)
			}
			if dir, err = merge(ctx, dir, depDir); err != nil {
				return dir, "", err
			}
		}
		return dir, loaded.Self().SourceRootSubpath, nil
	}

	return &localClients{
		modulePath: modulePath,
		declared:   ws.ModuleClients,
		load: func(ctx context.Context, path string) (dagql.ObjectResult[*ModuleSource], error) {
			var client dagql.ObjectResult[*ModuleSource]
			tree, sourceRootPath, err := loadedFiles(ctx, path)
			if err != nil {
				return client, err
			}
			// A client served from this tree finds its own clients among its
			// files, with a config that declares them, since it has no
			// workspace to read them from.
			closure := map[string]bool{path: true}
			declared := map[string][]string{}
			pending := []string{path}
			for len(pending) > 0 {
				current := pending[0]
				pending = pending[1:]
				targets := ws.ModuleClients(current)
				if len(targets) == 0 {
					continue
				}
				declared[current] = targets
				for _, target := range targets {
					if !closure[target] {
						closure[target] = true
						pending = append(pending, target)
					}
				}
			}
			for _, member := range slices.Sorted(maps.Keys(closure)) {
				if member == path {
					continue
				}
				dir, _, err := loadedFiles(ctx, member)
				if err != nil {
					return client, fmt.Errorf("load local client %q: %w", member, err)
				}
				if tree, err = merge(ctx, tree, dir); err != nil {
					return client, err
				}
			}
			if len(declared) > 0 {
				if err := dag.Select(ctx, tree, &tree, dagql.Selector{
					Field: "withNewFile",
					Args: []dagql.NamedInput{
						{Name: "path", Value: dagql.String(workspace.ConfigFileName)},
						{Name: "contents", Value: dagql.String(workspace.LocalClientsConfig(declared))},
					},
				}); err != nil {
					return client, err
				}
			}
			err = dag.Select(ctx, tree, &client, dagql.Selector{
				Field: "asModuleSource",
				Args:  []dagql.NamedInput{{Name: "sourceRootPath", Value: dagql.String(sourceRootPath)}},
			})
			return client, err
		},
	}, nil
}

func treeLocalClients(ctx context.Context, tree dagql.ObjectResult[*Directory], sourceRootSubpath string) (*localClients, error) {
	statFS := &DirectoryStatFS{Dir: tree}
	declared, err := workspace.TreeModuleScopeLocalClients(ctx,
		func(ctx context.Context, path string) (string, bool, error) {
			return StatFSExists(ctx, statFS, path)
		},
		func(ctx context.Context, path string) ([]byte, error) {
			return DirectoryReadFile(ctx, tree, path)
		},
		sourceRootSubpath,
	)
	if err != nil {
		return nil, fmt.Errorf("read the module's workspace config: %w", err)
	}
	modulePath := filepath.ToSlash(filepath.Clean(sourceRootSubpath))
	if len(declared[modulePath]) == 0 {
		return nil, nil
	}

	dag, err := CurrentDagqlServer(ctx)
	if err != nil {
		return nil, err
	}
	return &localClients{
		modulePath: modulePath,
		declared: func(path string) []string {
			return declared[path]
		},
		load: func(ctx context.Context, path string) (dagql.ObjectResult[*ModuleSource], error) {
			var client dagql.ObjectResult[*ModuleSource]
			err := dag.Select(ctx, tree, &client, dagql.Selector{
				Field: "asModuleSource",
				Args:  []dagql.NamedInput{{Name: "sourceRootPath", Value: dagql.String(path)}},
			})
			return client, err
		},
	}, nil
}
