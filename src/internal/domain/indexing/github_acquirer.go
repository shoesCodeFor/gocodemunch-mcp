package indexing

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultGitHubAPIBaseURL       = "https://api.github.com"
	defaultGitHubFetchConcurrency = 8
	defaultGitHubRequestTimeout   = 30 * time.Second
)

var (
	githubRepoSlugPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`)
	githubAllowedHosts    = map[string]struct{}{
		"github.com": {},
	}
	githubSourceSuffixes = []string{
		".al",
		".blade.php",
		".c",
		".cc",
		".cpp",
		".cs",
		".cshtml",
		".cxx",
		".dart",
		".ex",
		".go",
		".h",
		".hpp",
		".java",
		".js",
		".jsx",
		".php",
		".pl",
		".py",
		".rb",
		".rs",
		".sql",
		".swift",
		".ts",
		".tsx",
		".vue",
		".xml",
		".xul",
	}
	githubSkippedDirectories = map[string]struct{}{
		".git":         {},
		".hg":          {},
		".svn":         {},
		".venv":        {},
		"venv":         {},
		"node_modules": {},
		"dist":         {},
		"build":        {},
		"target":       {},
		"coverage":     {},
		"__pycache__":  {},
	}
	errGitHubContentNotFound = errors.New("github content not found")
)

type githubTreeResponse struct {
	SHA  string            `json:"sha"`
	Tree []githubTreeEntry `json:"tree"`
}

type githubTreeEntry struct {
	Path string `json:"path"`
	Type string `json:"type"`
	SHA  string `json:"sha"`
	Size int64  `json:"size"`
}

type githubErrorResponse struct {
	Message string `json:"message"`
}

// GitHubRepoAcquirer fetches GitHub repository trees and file contents.
type GitHubRepoAcquirer struct {
	client             *http.Client
	apiBaseURL         string
	token              string
	maxConcurrentFetch int
}

// GitHubRepoAcquirerOption customizes GitHubRepoAcquirer construction.
type GitHubRepoAcquirerOption func(*GitHubRepoAcquirer)

// WithGitHubHTTPClient overrides the HTTP client used for GitHub API calls.
func WithGitHubHTTPClient(client *http.Client) GitHubRepoAcquirerOption {
	return func(acquirer *GitHubRepoAcquirer) {
		if client != nil {
			acquirer.client = client
		}
	}
}

// WithGitHubAPIBaseURL overrides the GitHub API base URL.
func WithGitHubAPIBaseURL(baseURL string) GitHubRepoAcquirerOption {
	return func(acquirer *GitHubRepoAcquirer) {
		if strings.TrimSpace(baseURL) != "" {
			acquirer.apiBaseURL = normalizeGitHubAPIBaseURL(baseURL)
		}
	}
}

// WithGitHubToken sets a GitHub API token.
func WithGitHubToken(token string) GitHubRepoAcquirerOption {
	return func(acquirer *GitHubRepoAcquirer) {
		acquirer.token = strings.TrimSpace(token)
	}
}

// WithGitHubFetchConcurrency configures bounded concurrent file fetches.
func WithGitHubFetchConcurrency(limit int) GitHubRepoAcquirerOption {
	return func(acquirer *GitHubRepoAcquirer) {
		if limit > 0 {
			acquirer.maxConcurrentFetch = limit
		}
	}
}

// NewGitHubRepoAcquirer creates a GitHub API-backed repo acquirer.
func NewGitHubRepoAcquirer(optionFns ...GitHubRepoAcquirerOption) *GitHubRepoAcquirer {
	acquirer := &GitHubRepoAcquirer{
		client: &http.Client{
			Timeout: defaultGitHubRequestTimeout,
		},
		apiBaseURL:         defaultGitHubAPIBaseURL,
		maxConcurrentFetch: defaultGitHubFetchConcurrency,
	}
	for _, option := range optionFns {
		option(acquirer)
	}
	acquirer.apiBaseURL = normalizeGitHubAPIBaseURL(acquirer.apiBaseURL)
	if acquirer.maxConcurrentFetch <= 0 {
		acquirer.maxConcurrentFetch = defaultGitHubFetchConcurrency
	}
	if acquirer.client == nil {
		acquirer.client = &http.Client{Timeout: defaultGitHubRequestTimeout}
	}
	return acquirer
}

// NewGitHubRepoAcquirerFromEnv builds a GitHubRepoAcquirer from env vars.
// Returns nil when remote index_repo acquisition is explicitly disabled.
func NewGitHubRepoAcquirerFromEnv() *GitHubRepoAcquirer {
	if !parseEnvBool("GOCODEMUNCH_ENABLE_REMOTE_INDEX_REPO", true) {
		return nil
	}

	options := []GitHubRepoAcquirerOption{}
	if rawBaseURL := strings.TrimSpace(os.Getenv("GOCODEMUNCH_GITHUB_API_BASE_URL")); rawBaseURL != "" {
		options = append(options, WithGitHubAPIBaseURL(rawBaseURL))
	}

	token := strings.TrimSpace(os.Getenv("GOCODEMUNCH_GITHUB_TOKEN"))
	if token == "" {
		token = strings.TrimSpace(os.Getenv("GITHUB_TOKEN"))
	}
	if token != "" {
		options = append(options, WithGitHubToken(token))
	}

	if rawConcurrency := strings.TrimSpace(os.Getenv("GOCODEMUNCH_GITHUB_FETCH_CONCURRENCY")); rawConcurrency != "" {
		if parsed, err := strconv.Atoi(rawConcurrency); err == nil && parsed > 0 {
			options = append(options, WithGitHubFetchConcurrency(parsed))
		}
	}

	return NewGitHubRepoAcquirer(options...)
}

// AcquireTree fetches source-file content for a GitHub repository.
func (a *GitHubRepoAcquirer) AcquireTree(ctx context.Context, repoURL string) (map[string][]byte, error) {
	owner, repo, err := parseGitHubRepoReference(repoURL)
	if err != nil {
		return nil, err
	}

	metadata, err := a.AcquireTreeMetadata(ctx, repoURL)
	if err != nil {
		return nil, err
	}

	candidates := make([]string, 0, len(metadata.Files))
	for relPath := range metadata.Files {
		candidates = append(candidates, relPath)
	}
	return a.acquireTreePaths(ctx, owner, repo, candidates)
}

// AcquireTreeSubset fetches source-file content for a selected set of repository paths.
func (a *GitHubRepoAcquirer) AcquireTreeSubset(
	ctx context.Context,
	repoURL string,
	relPaths []string,
) (map[string][]byte, error) {
	owner, repo, err := parseGitHubRepoReference(repoURL)
	if err != nil {
		return nil, err
	}
	return a.acquireTreePaths(ctx, owner, repo, relPaths)
}

func (a *GitHubRepoAcquirer) acquireTreePaths(
	ctx context.Context,
	owner string,
	repo string,
	relPaths []string,
) (map[string][]byte, error) {
	candidates := normalizeGitHubContentPaths(relPaths)
	if len(candidates) == 0 {
		return map[string][]byte{}, nil
	}

	sort.Strings(candidates)

	tree := make(map[string][]byte, len(candidates))
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
	)
	setErr := func(err error) {
		if err == nil {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		if firstErr == nil {
			firstErr = err
		}
	}

	limit := a.maxConcurrentFetch
	if limit <= 0 {
		limit = defaultGitHubFetchConcurrency
	}
	semaphore := make(chan struct{}, limit)

	for _, relPath := range candidates {
		relPath := relPath
		wg.Add(1)
		go func() {
			defer wg.Done()

			select {
			case semaphore <- struct{}{}:
			case <-ctx.Done():
				setErr(ctx.Err())
				return
			}
			defer func() {
				<-semaphore
			}()

			content, fetchErr := a.fetchFileContent(ctx, owner, repo, relPath, false)
			if fetchErr != nil {
				if errors.Is(fetchErr, errGitHubContentNotFound) {
					return
				}
				setErr(fetchErr)
				return
			}
			if len(content) == 0 {
				return
			}

			cloned := make([]byte, len(content))
			copy(cloned, content)

			mu.Lock()
			tree[relPath] = cloned
			mu.Unlock()
		}()
	}
	wg.Wait()

	if len(tree) == 0 {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if firstErr != nil {
			return nil, firstErr
		}
	}

	return tree, nil
}

// AcquireTreeMetadata fetches source-file metadata for a GitHub repository.
func (a *GitHubRepoAcquirer) AcquireTreeMetadata(ctx context.Context, repoURL string) (RepoTreeMetadata, error) {
	owner, repo, err := parseGitHubRepoReference(repoURL)
	if err != nil {
		return RepoTreeMetadata{}, err
	}

	treeSHA, treeEntries, err := a.fetchRepoTree(ctx, owner, repo)
	if err != nil {
		return RepoTreeMetadata{}, err
	}

	files := map[string]RemoteFileMetadata{}
	for _, entry := range treeEntries {
		if entry.Type != "blob" {
			continue
		}
		relPath, ok := normalizeGitHubTreePath(entry.Path)
		if !ok {
			continue
		}
		if !shouldDownloadGitHubPath(relPath) {
			continue
		}
		files[relPath] = RemoteFileMetadata{
			BlobSHA:   strings.ToLower(strings.TrimSpace(entry.SHA)),
			SizeBytes: entry.Size,
		}
	}

	return RepoTreeMetadata{
		TreeSHA: strings.ToLower(strings.TrimSpace(treeSHA)),
		Files:   files,
	}, nil
}

// ReadGitignore fetches repository root .gitignore content when present.
func (a *GitHubRepoAcquirer) ReadGitignore(ctx context.Context, repoURL string) ([]byte, error) {
	owner, repo, err := parseGitHubRepoReference(repoURL)
	if err != nil {
		return nil, err
	}
	content, err := a.fetchFileContent(ctx, owner, repo, ".gitignore", true)
	if err != nil {
		return nil, err
	}
	if len(content) == 0 {
		return nil, nil
	}
	cloned := make([]byte, len(content))
	copy(cloned, content)
	return cloned, nil
}

func (a *GitHubRepoAcquirer) fetchRepoTree(ctx context.Context, owner, repo string) (string, []githubTreeEntry, error) {
	endpoint := fmt.Sprintf(
		"%s/repos/%s/%s/git/trees/HEAD?recursive=1",
		a.apiBaseURL,
		url.PathEscape(owner),
		url.PathEscape(repo),
	)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", nil, err
	}
	request.Header.Set("Accept", "application/vnd.github.v3+json")
	if strings.TrimSpace(a.token) != "" {
		request.Header.Set("Authorization", "token "+a.token)
	}

	response, err := a.client.Do(request)
	if err != nil {
		return "", nil, err
	}
	defer response.Body.Close()

	switch response.StatusCode {
	case http.StatusNotFound:
		return "", nil, fmt.Errorf("Repository not found: %s/%s", owner, repo)
	case http.StatusForbidden, http.StatusTooManyRequests:
		if strings.TrimSpace(a.token) == "" {
			return "", nil, errors.New("GitHub API rate limit exceeded. Set GITHUB_TOKEN.")
		}
		return "", nil, errors.New("GitHub API rate limit exceeded.")
	}
	if response.StatusCode >= 400 {
		return "", nil, fmt.Errorf(
			"GitHub API request failed (%d): %s",
			response.StatusCode,
			readGitHubErrorMessage(response.Body),
		)
	}

	var payload githubTreeResponse
	if err := json.NewDecoder(io.LimitReader(response.Body, 32<<20)).Decode(&payload); err != nil {
		return "", nil, fmt.Errorf("decode GitHub tree response: %w", err)
	}
	return payload.SHA, payload.Tree, nil
}

func (a *GitHubRepoAcquirer) fetchFileContent(
	ctx context.Context,
	owner string,
	repo string,
	filePath string,
	allowMissing bool,
) ([]byte, error) {
	endpoint := fmt.Sprintf(
		"%s/repos/%s/%s/contents/%s",
		a.apiBaseURL,
		url.PathEscape(owner),
		url.PathEscape(repo),
		escapeGitHubContentPath(filePath),
	)
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Accept", "application/vnd.github.v3.raw")
	if strings.TrimSpace(a.token) != "" {
		request.Header.Set("Authorization", "token "+a.token)
	}

	response, err := a.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()

	switch response.StatusCode {
	case http.StatusNotFound:
		if allowMissing {
			return nil, nil
		}
		return nil, errGitHubContentNotFound
	case http.StatusForbidden, http.StatusTooManyRequests:
		if strings.TrimSpace(a.token) == "" {
			return nil, errors.New("GitHub API rate limit exceeded. Set GITHUB_TOKEN.")
		}
		return nil, errors.New("GitHub API rate limit exceeded.")
	}
	if response.StatusCode >= 400 {
		return nil, fmt.Errorf(
			"GitHub content request failed (%d): %s",
			response.StatusCode,
			readGitHubErrorMessage(response.Body),
		)
	}

	content, err := io.ReadAll(io.LimitReader(response.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	return content, nil
}

func parseGitHubRepoReference(raw string) (string, string, error) {
	trimmed := strings.TrimSpace(strings.TrimSuffix(raw, ".git"))
	if trimmed == "" {
		return "", "", fmt.Errorf("Could not parse GitHub URL: %s", raw)
	}

	if strings.Contains(trimmed, "/") && !strings.Contains(trimmed, "://") {
		parts := strings.Split(trimmed, "/")
		if len(parts) >= 2 {
			return parseGitHubOwnerRepo(parts[0], parts[1], trimmed)
		}
	}

	parsed, err := url.Parse(trimmed)
	if err != nil {
		return "", "", fmt.Errorf("Could not parse GitHub URL: %s", raw)
	}

	host := strings.ToLower(strings.TrimSpace(parsed.Hostname()))
	if _, ok := githubAllowedHosts[host]; !ok {
		return "", "", fmt.Errorf("Unsupported host %q. Only github.com URLs are accepted.", host)
	}

	pathParts := strings.Split(strings.Trim(parsed.Path, "/"), "/")
	if len(pathParts) < 2 {
		return "", "", fmt.Errorf("Could not parse GitHub URL: %s", raw)
	}
	return parseGitHubOwnerRepo(pathParts[0], pathParts[1], raw)
}

func parseGitHubOwnerRepo(owner, repo, input string) (string, string, error) {
	owner = strings.TrimSpace(owner)
	repo = strings.TrimSpace(repo)
	if owner == "" || repo == "" {
		return "", "", fmt.Errorf("Could not parse GitHub URL: %s", input)
	}
	if !githubRepoSlugPattern.MatchString(owner) || !githubRepoSlugPattern.MatchString(repo) {
		return "", "", fmt.Errorf("Invalid owner/repo format: %q", input)
	}
	return owner, repo, nil
}

func normalizeGitHubTreePath(rawPath string) (string, bool) {
	trimmed := strings.TrimSpace(strings.ReplaceAll(rawPath, "\\", "/"))
	trimmed = strings.TrimPrefix(trimmed, "./")
	trimmed = strings.Trim(trimmed, "/")
	if trimmed == "" {
		return "", false
	}

	cleaned := path.Clean(trimmed)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") || strings.Contains(cleaned, "/../") {
		return "", false
	}
	return cleaned, true
}

func normalizeGitHubContentPaths(rawPaths []string) []string {
	if len(rawPaths) == 0 {
		return nil
	}

	seen := map[string]struct{}{}
	normalized := make([]string, 0, len(rawPaths))
	for _, rawPath := range rawPaths {
		relPath, ok := normalizeGitHubTreePath(rawPath)
		if !ok {
			continue
		}
		if !shouldDownloadGitHubPath(relPath) {
			continue
		}
		if _, exists := seen[relPath]; exists {
			continue
		}
		seen[relPath] = struct{}{}
		normalized = append(normalized, relPath)
	}
	sort.Strings(normalized)
	return normalized
}

func shouldDownloadGitHubPath(relPath string) bool {
	for _, segment := range strings.Split(relPath, "/") {
		if _, skip := githubSkippedDirectories[segment]; skip {
			return false
		}
	}

	lower := strings.ToLower(relPath)
	for _, suffix := range githubSourceSuffixes {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}
	return false
}

func escapeGitHubContentPath(rawPath string) string {
	trimmed := strings.Trim(strings.TrimSpace(rawPath), "/")
	if trimmed == "" {
		return ""
	}
	parts := strings.Split(trimmed, "/")
	for idx := range parts {
		parts[idx] = url.PathEscape(parts[idx])
	}
	return strings.Join(parts, "/")
}

func readGitHubErrorMessage(body io.Reader) string {
	payload, err := io.ReadAll(io.LimitReader(body, 1<<20))
	if err != nil {
		return "unknown error"
	}
	if len(payload) == 0 {
		return "unknown error"
	}

	var decoded githubErrorResponse
	if err := json.Unmarshal(payload, &decoded); err == nil && strings.TrimSpace(decoded.Message) != "" {
		return strings.TrimSpace(decoded.Message)
	}
	return strings.TrimSpace(string(payload))
}

func normalizeGitHubAPIBaseURL(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return defaultGitHubAPIBaseURL
	}
	trimmed = strings.TrimRight(trimmed, "/")
	if !strings.Contains(trimmed, "://") {
		return "https://" + trimmed
	}
	return trimmed
}

func parseEnvBool(key string, fallback bool) bool {
	raw := strings.ToLower(strings.TrimSpace(os.Getenv(key)))
	if raw == "" {
		return fallback
	}
	switch raw {
	case "0", "false", "no", "off":
		return false
	case "1", "true", "yes", "on":
		return true
	default:
		return fallback
	}
}
