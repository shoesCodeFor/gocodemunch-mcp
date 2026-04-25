package orchestration

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/jgravelle/gocodemunch-mcp/src/internal/config"
	"github.com/jgravelle/gocodemunch-mcp/src/internal/storage"
)

func (s *Service) repoScopedConfig(ctx context.Context, arguments map[string]any) config.Config {
	repoArg := strings.TrimSpace(stringArg(arguments, "repo", ""))
	if repoArg == "" {
		return s.cfg
	}

	sourceRoot, ok := sourceRootForRepoArgument(ctx, s.deps.IndexStore, repoArg)
	if !ok {
		return s.cfg
	}

	return s.cfg.ForProjectRoot(sourceRoot)
}

func sourceRootForRepoArgument(
	ctx context.Context,
	store storage.IndexStore,
	repo string,
) (string, bool) {
	repo = strings.TrimSpace(repo)
	if repo == "" || store == nil {
		return "", false
	}

	repos, err := store.List(ctx)
	if err != nil {
		return "", false
	}

	if strings.Contains(repo, "/") {
		target := filepath.Clean(repo)
		for _, entry := range repos {
			sourceRoot := strings.TrimSpace(entry.SourceRoot)
			if sourceRoot == "" {
				continue
			}

			if entry.Repo == repo || entry.DisplayName == repo || filepath.Clean(sourceRoot) == target {
				return sourceRoot, true
			}
		}
		return "", false
	}

	matches := make([]string, 0, 2)
	for _, entry := range repos {
		owner, repoName, ok := strings.Cut(strings.TrimSpace(entry.Repo), "/")
		if !ok || owner == "" || repoName == "" {
			continue
		}
		if repoName != repo && entry.DisplayName != repo {
			continue
		}
		sourceRoot := strings.TrimSpace(entry.SourceRoot)
		if sourceRoot == "" {
			continue
		}
		matches = append(matches, sourceRoot)
	}

	if len(matches) != 1 {
		return "", false
	}
	return matches[0], true
}
