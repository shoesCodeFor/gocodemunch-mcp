package config

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const (
	defaultServerName           = "gocodemunch-mcp"
	defaultServerVersion        = "0.1.0"
	defaultFreshnessMode        = "relaxed"
	defaultFanoutMode           = "serial"
	defaultFanoutOverloadPolicy = "reject"
	defaultVectorBackend        = "sqlite"
	defaultEmbeddingProvider    = "ollama"
	defaultEmbeddingModel       = "bge-m3"
	defaultOllamaBaseURL        = "http://host.docker.internal:11434"
	defaultVLLMBaseURL          = "http://localhost:8000/v1"
	defaultVLLMModel            = "bge-m3"
	defaultQdrantURL            = "http://localhost:6333"
	defaultQdrantCollection     = "gocodemunch_vectors"
	defaultStorageDirName       = ".code-index"
	globalConfigFileName        = "config.jsonc"
	projectConfigFileName       = ".jcodemunch.jsonc"

	defaultFanoutMaxWorkers     = 4
	defaultFanoutMaxQueueDepth  = 256
	defaultRequestTimeoutMS     = 0
	defaultVectorTopK           = 5
	defaultVectorQueryTimeoutMS = 8000
	defaultVectorLexicalWeight  = 0.5
	defaultVectorSemanticWeight = 0.5
	defaultFanoutItemTimeoutMS  = 0
	defaultMaxFolderFiles       = 2000
	defaultMaxIndexFiles        = 10000
)

// Config holds process-level server configuration.
type Config struct {
	ServerName    string
	ServerVersion string
	StoragePath   string
	// Languages constrains enabled language surface. nil means all languages.
	Languages     []string
	FreshnessMode string
	FanoutMode    string
	// FanoutMaxWorkers bounds concurrent batch item execution when fanout mode
	// is set to parallel.
	FanoutMaxWorkers int
	// FanoutMaxQueueDepth caps per-request batch item queue size to keep memory
	// growth explicit and bounded.
	FanoutMaxQueueDepth int
	// FanoutOverloadPolicy controls queue-depth overflow behavior for batch
	// requests. "reject" returns an overload error; "degrade" falls back to
	// serial execution.
	FanoutOverloadPolicy string
	// RequestTimeoutMS bounds total tool execution time in milliseconds.
	// Zero disables request timeout budgeting.
	RequestTimeoutMS int
	// VectorBackend selects the vector storage backend implementation.
	VectorBackend string
	// VectorTopK controls the default number of nearest-neighbor results.
	VectorTopK int
	// VectorQueryTimeoutMS bounds vector query/embedding calls in milliseconds.
	VectorQueryTimeoutMS int
	// VectorLexicalWeight controls lexical score contribution in hybrid retrieval.
	VectorLexicalWeight float64
	// VectorSemanticWeight controls semantic/vector score contribution in hybrid retrieval.
	VectorSemanticWeight float64
	// EmbeddingProvider selects which embedding service implementation to use.
	EmbeddingProvider string
	// EmbeddingModel is the provider model identifier for embeddings.
	EmbeddingModel string
	// OllamaBaseURL is the HTTP base URL for Ollama embedding requests.
	OllamaBaseURL string
	// VLLMBaseURL is the HTTP base URL for vLLM OpenAI-compatible embeddings.
	VLLMBaseURL string
	// VLLMModel is the model identifier used for vLLM embedding requests.
	VLLMModel string
	// VLLMAPIKey is an optional API key used for authenticated vLLM access.
	VLLMAPIKey string
	// QdrantURL is the HTTP base URL for Qdrant vector storage operations.
	QdrantURL string
	// QdrantAPIKey is an optional API key used for authenticated Qdrant access.
	QdrantAPIKey string
	// QdrantCollection is the target Qdrant collection name for vector records.
	QdrantCollection string
	// FanoutItemTimeoutMS bounds each batch item execution in milliseconds.
	// Zero disables per-item timeout budgeting.
	FanoutItemTimeoutMS int
	// nil => default behavior (inject empty _meta when absent)
	// empty slice => strip _meta
	// non-empty => include only listed _meta fields
	MetaFields []string
	// MaxFolderFiles caps files indexed for local folder discovery.
	MaxFolderFiles int
	// MaxIndexFiles caps files indexed for remote repository discovery.
	MaxIndexFiles int
	// ExtraIgnorePatterns are merged with per-call ignore patterns.
	ExtraIgnorePatterns []string
	// ExcludeSecretPatterns disables matching for specific secret patterns.
	ExcludeSecretPatterns []string
	Disabled              map[string]struct{}
	// TrustedFolders constrains local indexing roots by containment policy.
	TrustedFolders []string
	// nil => default whitelist mode (true), non-nil => explicit whitelist/blacklist mode.
	TrustedFoldersWhitelistMode *bool
}

type globalConfigSnapshot struct {
	Languages                      []string
	HasLanguages                   bool
	FreshnessMode                  string
	HasFreshnessMode               bool
	MetaFields                     []string
	HasMetaFields                  bool
	MaxFolderFiles                 int
	HasMaxFolderFiles              bool
	MaxIndexFiles                  int
	HasMaxIndexFiles               bool
	ExtraIgnorePatterns            []string
	HasExtraIgnorePatterns         bool
	ExcludeSecretPatterns          []string
	HasExcludeSecretPatterns       bool
	DisabledTools                  []string
	HasDisabledTools               bool
	TrustedFolders                 []string
	HasTrustedFolders              bool
	TrustedFoldersWhitelistMode    bool
	HasTrustedFoldersWhitelistMode bool
}

// Load reads environment-backed configuration.
func Load() (Config, error) {
	storagePath := strings.TrimSpace(os.Getenv("CODE_INDEX_PATH"))
	snapshot := loadGlobalConfigSnapshot(storagePath)

	cfg := Config{
		ServerName:    getenv("GOCODEMUNCH_SERVER_NAME", defaultServerName),
		ServerVersion: getenv("GOCODEMUNCH_SERVER_VERSION", defaultServerVersion),
		StoragePath:   storagePath,
		FanoutMode:    parseFanoutMode(os.Getenv("GOCODEMUNCH_FANOUT_MODE")),
		FanoutOverloadPolicy: parseFanoutOverloadPolicy(
			os.Getenv("GOCODEMUNCH_FANOUT_OVERLOAD_POLICY"),
		),
		FanoutMaxWorkers: parsePositiveInt(
			os.Getenv("GOCODEMUNCH_FANOUT_MAX_WORKERS"),
			defaultFanoutMaxWorkers,
		),
		FanoutMaxQueueDepth: parsePositiveInt(
			os.Getenv("GOCODEMUNCH_FANOUT_MAX_QUEUE_DEPTH"),
			defaultFanoutMaxQueueDepth,
		),
		RequestTimeoutMS: parseNonNegativeInt(
			os.Getenv("GOCODEMUNCH_REQUEST_TIMEOUT_MS"),
			defaultRequestTimeoutMS,
		),
		VectorBackend:        defaultVectorBackend,
		VectorTopK:           defaultVectorTopK,
		VectorQueryTimeoutMS: defaultVectorQueryTimeoutMS,
		VectorLexicalWeight:  defaultVectorLexicalWeight,
		VectorSemanticWeight: defaultVectorSemanticWeight,
		EmbeddingProvider:    defaultEmbeddingProvider,
		EmbeddingModel:       defaultEmbeddingModel,
		OllamaBaseURL:        defaultOllamaBaseURL,
		VLLMBaseURL:          defaultVLLMBaseURL,
		VLLMModel:            defaultVLLMModel,
		QdrantURL:            defaultQdrantURL,
		QdrantCollection:     defaultQdrantCollection,
		FanoutItemTimeoutMS: parseNonNegativeInt(
			os.Getenv("GOCODEMUNCH_FANOUT_ITEM_TIMEOUT_MS"),
			defaultFanoutItemTimeoutMS,
		),
		MaxFolderFiles: defaultMaxFolderFiles,
		MaxIndexFiles:  defaultMaxIndexFiles,
		Disabled:       map[string]struct{}{},
	}

	if err := applyVectorEnvOverrides(&cfg); err != nil {
		return cfg, err
	}

	if snapshot.HasLanguages {
		cfg.Languages = snapshot.Languages
	} else {
		cfg.Languages = parseLanguageListEnv("GOCODEMUNCH_LANGUAGES", "JCODEMUNCH_LANGUAGES")
	}

	if snapshot.HasFreshnessMode {
		cfg.FreshnessMode = parseFreshnessMode(snapshot.FreshnessMode)
	} else {
		cfg.FreshnessMode = parseFreshnessMode(os.Getenv("GOCODEMUNCH_FRESHNESS_MODE"))
	}

	if snapshot.HasMetaFields {
		cfg.MetaFields = snapshot.MetaFields
	} else if raw := strings.TrimSpace(os.Getenv("GOCODEMUNCH_META_FIELDS")); raw != "" {
		if strings.EqualFold(raw, "none") {
			cfg.MetaFields = []string{}
		} else {
			cfg.MetaFields = splitCSV(raw)
		}
	}

	if snapshot.HasMaxFolderFiles {
		cfg.MaxFolderFiles = snapshot.MaxFolderFiles
	} else {
		cfg.MaxFolderFiles = parsePositiveIntEnv(
			defaultMaxFolderFiles,
			"GOCODEMUNCH_MAX_FOLDER_FILES",
			"JCODEMUNCH_MAX_FOLDER_FILES",
		)
	}

	if snapshot.HasMaxIndexFiles {
		cfg.MaxIndexFiles = snapshot.MaxIndexFiles
	} else {
		cfg.MaxIndexFiles = parsePositiveIntEnv(
			defaultMaxIndexFiles,
			"GOCODEMUNCH_MAX_INDEX_FILES",
			"JCODEMUNCH_MAX_INDEX_FILES",
		)
	}

	if snapshot.HasExtraIgnorePatterns {
		cfg.ExtraIgnorePatterns = cloneStringSlice(snapshot.ExtraIgnorePatterns)
	} else {
		cfg.ExtraIgnorePatterns = parsePathListEnv(
			"GOCODEMUNCH_EXTRA_IGNORE_PATTERNS",
			"JCODEMUNCH_EXTRA_IGNORE_PATTERNS",
		)
	}

	if snapshot.HasExcludeSecretPatterns {
		cfg.ExcludeSecretPatterns = cloneStringSlice(snapshot.ExcludeSecretPatterns)
	} else {
		cfg.ExcludeSecretPatterns = parsePathListEnv(
			"GOCODEMUNCH_EXCLUDE_SECRET_PATTERNS",
			"JCODEMUNCH_EXCLUDE_SECRET_PATTERNS",
		)
	}

	if snapshot.HasTrustedFolders {
		cfg.TrustedFolders = cloneStringSlice(snapshot.TrustedFolders)
	} else {
		cfg.TrustedFolders = parseTrustedFoldersEnv(
			"GOCODEMUNCH_TRUSTED_FOLDERS",
			"JCODEMUNCH_TRUSTED_FOLDERS",
		)
	}

	if snapshot.HasTrustedFoldersWhitelistMode {
		cfg.TrustedFoldersWhitelistMode = boolPtr(snapshot.TrustedFoldersWhitelistMode)
	} else if value, ok := parseBooleanEnv(
		"GOCODEMUNCH_TRUSTED_FOLDERS_WHITELIST_MODE",
		"JCODEMUNCH_TRUSTED_FOLDERS_WHITELIST_MODE",
	); ok {
		cfg.TrustedFoldersWhitelistMode = boolPtr(value)
	}

	disabledToolNames := snapshot.DisabledTools
	if !snapshot.HasDisabledTools {
		disabledToolNames = splitCSV(os.Getenv("GOCODEMUNCH_DISABLED_TOOLS"))
	}
	for _, name := range disabledToolNames {
		if name == "" {
			continue
		}
		cfg.Disabled[name] = struct{}{}
	}

	return cfg, nil
}

// MustLoad reads environment-backed configuration and panics when validation
// fails. Startup paths that cannot return config errors directly should call
// this helper to remain fail-fast.
func MustLoad() Config {
	cfg, err := Load()
	if err != nil {
		panic(err)
	}
	return cfg
}

// ForProjectRoot overlays project-level .jcodemunch.jsonc settings onto an
// already-loaded process config. Missing or invalid project keys are ignored.
func (c Config) ForProjectRoot(sourceRoot string) Config {
	snapshot := loadProjectConfigSnapshot(sourceRoot)
	return c.withSnapshot(snapshot)
}

// TrustedFoldersWhitelistEnabled reports whether whitelist mode is active.
// Default behavior is whitelist mode when the key is not explicitly configured.
func (c Config) TrustedFoldersWhitelistEnabled() bool {
	if c.TrustedFoldersWhitelistMode == nil {
		return true
	}
	return *c.TrustedFoldersWhitelistMode
}

// IsToolDisabled reports whether a tool should be hidden and rejected.
func (c Config) IsToolDisabled(name string) bool {
	if name == "search_columns" && !c.IsLanguageEnabled("sql") {
		return true
	}
	_, ok := c.Disabled[name]
	return ok
}

// IsLanguageEnabled reports whether a language is enabled by config.
func (c Config) IsLanguageEnabled(language string) bool {
	language = strings.ToLower(strings.TrimSpace(language))
	if language == "" {
		return true
	}
	if c.Languages == nil {
		return true
	}
	for _, candidate := range c.Languages {
		if candidate == language {
			return true
		}
	}
	return false
}

func (c Config) clone() Config {
	cloned := c
	cloned.Languages = cloneStringSlice(c.Languages)
	cloned.MetaFields = cloneStringSlice(c.MetaFields)
	cloned.ExtraIgnorePatterns = cloneStringSlice(c.ExtraIgnorePatterns)
	cloned.ExcludeSecretPatterns = cloneStringSlice(c.ExcludeSecretPatterns)
	cloned.Disabled = cloneStringSet(c.Disabled)
	cloned.TrustedFolders = cloneStringSlice(c.TrustedFolders)
	cloned.TrustedFoldersWhitelistMode = cloneBoolPtr(c.TrustedFoldersWhitelistMode)
	return cloned
}

func (c Config) withSnapshot(snapshot globalConfigSnapshot) Config {
	cfg := c.clone()

	if snapshot.HasLanguages {
		cfg.Languages = cloneStringSlice(snapshot.Languages)
	}

	if snapshot.HasFreshnessMode {
		cfg.FreshnessMode = parseFreshnessMode(snapshot.FreshnessMode)
	}

	if snapshot.HasMetaFields {
		cfg.MetaFields = cloneStringSlice(snapshot.MetaFields)
	}

	if snapshot.HasMaxFolderFiles {
		cfg.MaxFolderFiles = snapshot.MaxFolderFiles
	}

	if snapshot.HasMaxIndexFiles {
		cfg.MaxIndexFiles = snapshot.MaxIndexFiles
	}

	if snapshot.HasExtraIgnorePatterns {
		cfg.ExtraIgnorePatterns = cloneStringSlice(snapshot.ExtraIgnorePatterns)
	}

	if snapshot.HasExcludeSecretPatterns {
		cfg.ExcludeSecretPatterns = cloneStringSlice(snapshot.ExcludeSecretPatterns)
	}

	if snapshot.HasTrustedFolders {
		cfg.TrustedFolders = cloneStringSlice(snapshot.TrustedFolders)
	}

	if snapshot.HasTrustedFoldersWhitelistMode {
		cfg.TrustedFoldersWhitelistMode = boolPtr(snapshot.TrustedFoldersWhitelistMode)
	}

	if snapshot.HasDisabledTools {
		cfg.Disabled = map[string]struct{}{}
		for _, toolName := range snapshot.DisabledTools {
			if toolName == "" {
				continue
			}
			cfg.Disabled[toolName] = struct{}{}
		}
	}

	return cfg
}

func getenv(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func applyVectorEnvOverrides(cfg *Config) error {
	if cfg == nil {
		return nil
	}

	validationErrors := []string{}

	if raw, ok := getenvTrimmed("VECTOR_BACKEND"); ok {
		backend := strings.ToLower(raw)
		switch backend {
		case "sqlite", "qdrant":
			cfg.VectorBackend = backend
		default:
			validationErrors = append(
				validationErrors,
				fmt.Sprintf(
					`VECTOR_BACKEND must be one of ["sqlite", "qdrant"] (got %q); set VECTOR_BACKEND=sqlite`,
					raw,
				),
			)
		}
	}

	if raw, ok := getenvTrimmed("VECTOR_TOP_K"); ok {
		value, err := strconv.Atoi(raw)
		if err != nil || value <= 0 {
			validationErrors = append(
				validationErrors,
				fmt.Sprintf(
					"VECTOR_TOP_K must be a positive integer (got %q); set VECTOR_TOP_K=%d",
					raw,
					defaultVectorTopK,
				),
			)
		} else {
			cfg.VectorTopK = value
		}
	}

	if raw, ok := getenvTrimmed("VECTOR_QUERY_TIMEOUT_MS"); ok {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 0 {
			validationErrors = append(
				validationErrors,
				fmt.Sprintf(
					"VECTOR_QUERY_TIMEOUT_MS must be a non-negative integer in milliseconds (got %q); set VECTOR_QUERY_TIMEOUT_MS=%d",
					raw,
					defaultVectorQueryTimeoutMS,
				),
			)
		} else {
			cfg.VectorQueryTimeoutMS = value
		}
	}

	if raw, ok := getenvTrimmed("VECTOR_LEXICAL_WEIGHT"); ok {
		value, err := strconv.ParseFloat(raw, 64)
		if err != nil || value < 0 {
			validationErrors = append(
				validationErrors,
				fmt.Sprintf(
					"VECTOR_LEXICAL_WEIGHT must be a non-negative number (got %q); set VECTOR_LEXICAL_WEIGHT=%g",
					raw,
					defaultVectorLexicalWeight,
				),
			)
		} else {
			cfg.VectorLexicalWeight = value
		}
	}

	if raw, ok := getenvTrimmed("VECTOR_SEMANTIC_WEIGHT"); ok {
		value, err := strconv.ParseFloat(raw, 64)
		if err != nil || value < 0 {
			validationErrors = append(
				validationErrors,
				fmt.Sprintf(
					"VECTOR_SEMANTIC_WEIGHT must be a non-negative number (got %q); set VECTOR_SEMANTIC_WEIGHT=%g",
					raw,
					defaultVectorSemanticWeight,
				),
			)
		} else {
			cfg.VectorSemanticWeight = value
		}
	}

	if cfg.VectorLexicalWeight == 0 && cfg.VectorSemanticWeight == 0 {
		validationErrors = append(
			validationErrors,
			fmt.Sprintf(
				"VECTOR_LEXICAL_WEIGHT and VECTOR_SEMANTIC_WEIGHT cannot both be 0; set at least one positive (for example VECTOR_LEXICAL_WEIGHT=%g, VECTOR_SEMANTIC_WEIGHT=%g)",
				defaultVectorLexicalWeight,
				defaultVectorSemanticWeight,
			),
		)
	}

	if raw, ok := getenvTrimmed("EMBEDDING_PROVIDER"); ok {
		provider := strings.ToLower(raw)
		switch provider {
		case "ollama", "vllm":
			cfg.EmbeddingProvider = provider
		default:
			validationErrors = append(
				validationErrors,
				fmt.Sprintf(
					`EMBEDDING_PROVIDER must be one of ["ollama", "vllm"] (got %q); set EMBEDDING_PROVIDER=ollama`,
					raw,
				),
			)
		}
	}

	if raw, ok := getenvTrimmed("EMBEDDING_MODEL"); ok {
		cfg.EmbeddingModel = raw
	}

	if raw, ok := getenvTrimmed("OLLAMA_BASE_URL"); ok {
		if !isHTTPBaseURL(raw) {
			validationErrors = append(
				validationErrors,
				fmt.Sprintf(
					"OLLAMA_BASE_URL must be an absolute HTTP URL (got %q); set OLLAMA_BASE_URL=%s",
					raw,
					defaultOllamaBaseURL,
				),
			)
		} else {
			cfg.OllamaBaseURL = raw
		}
	}

	if raw, ok := getenvTrimmed("VLLM_BASE_URL"); ok {
		cfg.VLLMBaseURL = raw
	}

	if raw, ok := getenvTrimmed("VLLM_MODEL"); ok {
		cfg.VLLMModel = raw
	}

	if raw, ok := getenvTrimmed("VLLM_API_KEY"); ok {
		cfg.VLLMAPIKey = raw
	}

	if cfg.EmbeddingProvider == "vllm" {
		if strings.TrimSpace(cfg.VLLMBaseURL) == "" {
			validationErrors = append(
				validationErrors,
				fmt.Sprintf(
					"VLLM_BASE_URL is required when EMBEDDING_PROVIDER=vllm; set VLLM_BASE_URL=%s",
					defaultVLLMBaseURL,
				),
			)
		} else if !isHTTPBaseURL(cfg.VLLMBaseURL) {
			validationErrors = append(
				validationErrors,
				fmt.Sprintf(
					"VLLM_BASE_URL must be an absolute HTTP URL when EMBEDDING_PROVIDER=vllm (got %q); set VLLM_BASE_URL=%s",
					cfg.VLLMBaseURL,
					defaultVLLMBaseURL,
				),
			)
		}
		if strings.TrimSpace(cfg.VLLMModel) == "" {
			validationErrors = append(
				validationErrors,
				fmt.Sprintf(
					"VLLM_MODEL is required when EMBEDDING_PROVIDER=vllm; set VLLM_MODEL=%s",
					defaultVLLMModel,
				),
			)
		}
	}

	if raw, ok := getenvTrimmed("QDRANT_URL"); ok {
		if !isHTTPBaseURL(raw) {
			validationErrors = append(
				validationErrors,
				fmt.Sprintf(
					"QDRANT_URL must be an absolute HTTP URL (got %q); set QDRANT_URL=%s",
					raw,
					defaultQdrantURL,
				),
			)
		} else {
			cfg.QdrantURL = raw
		}
	}

	if raw, ok := getenvTrimmed("QDRANT_API_KEY"); ok {
		cfg.QdrantAPIKey = raw
	}

	if raw, ok := getenvTrimmed("QDRANT_COLLECTION"); ok {
		cfg.QdrantCollection = raw
	}

	if cfg.VectorBackend == "qdrant" {
		if strings.TrimSpace(cfg.QdrantURL) == "" {
			validationErrors = append(
				validationErrors,
				fmt.Sprintf(
					"QDRANT_URL is required when VECTOR_BACKEND=qdrant; set QDRANT_URL=%s",
					defaultQdrantURL,
				),
			)
		}
		if strings.TrimSpace(cfg.QdrantCollection) == "" {
			validationErrors = append(
				validationErrors,
				fmt.Sprintf(
					"QDRANT_COLLECTION is required when VECTOR_BACKEND=qdrant; set QDRANT_COLLECTION=%s",
					defaultQdrantCollection,
				),
			)
		}
	}

	if len(validationErrors) == 0 {
		return nil
	}
	return fmt.Errorf("vector configuration validation failed: %s", strings.Join(validationErrors, "; "))
}

func isHTTPBaseURL(raw string) bool {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return false
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
		return true
	default:
		return false
	}
}

func getenvTrimmed(key string) (string, bool) {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return "", false
	}
	return value, true
}

func splitCSV(raw string) []string {
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func parseBooleanEnv(keys ...string) (bool, bool) {
	for _, key := range keys {
		raw, ok := os.LookupEnv(key)
		if !ok {
			continue
		}
		value, parsed := parseBoolean(raw)
		if !parsed {
			return false, false
		}
		return value, true
	}
	return false, false
}

func parseBoolean(raw string) (bool, bool) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true, true
	case "0", "false", "no", "off":
		return false, true
	default:
		return false, false
	}
}

func parsePositiveIntEnv(fallback int, keys ...string) int {
	for _, key := range keys {
		raw, ok := os.LookupEnv(key)
		if !ok {
			continue
		}
		return parsePositiveInt(raw, fallback)
	}
	return fallback
}

func parseFreshnessMode(raw string) string {
	mode := strings.ToLower(strings.TrimSpace(raw))
	switch mode {
	case "strict":
		return "strict"
	case "", "relaxed":
		return defaultFreshnessMode
	default:
		return defaultFreshnessMode
	}
}

func parseFanoutMode(raw string) string {
	mode := strings.ToLower(strings.TrimSpace(raw))
	switch mode {
	case "parallel":
		return "parallel"
	case "", "serial":
		return defaultFanoutMode
	default:
		return defaultFanoutMode
	}
}

func parseFanoutOverloadPolicy(raw string) string {
	policy := strings.ToLower(strings.TrimSpace(raw))
	switch policy {
	case "degrade":
		return "degrade"
	case "", "reject":
		return defaultFanoutOverloadPolicy
	default:
		return defaultFanoutOverloadPolicy
	}
}

func parsePositiveInt(raw string, fallback int) int {
	value := strings.TrimSpace(raw)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return fallback
	}
	return parsed
}

func parseNonNegativeInt(raw string, fallback int) int {
	value := strings.TrimSpace(raw)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < 0 {
		return fallback
	}
	return parsed
}

func parseLanguageListEnv(keys ...string) []string {
	for _, key := range keys {
		raw, ok := os.LookupEnv(key)
		if !ok {
			continue
		}
		return parseLanguageList(raw)
	}
	return nil
}

func parseLanguageList(raw string) []string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}

	if strings.HasPrefix(trimmed, "[") {
		var values []string
		if err := json.Unmarshal([]byte(trimmed), &values); err == nil {
			return normalizeLanguageList(values)
		}
	}

	return normalizeLanguageList(strings.Split(trimmed, ","))
}

func parsePathList(raw string) []string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil
	}

	if strings.HasPrefix(trimmed, "[") {
		var values []string
		if err := json.Unmarshal([]byte(trimmed), &values); err == nil {
			return trimAndFilterNonEmpty(values)
		}
	}

	return trimAndFilterNonEmpty(strings.Split(trimmed, ","))
}

func parsePathListEnv(keys ...string) []string {
	for _, key := range keys {
		raw, ok := os.LookupEnv(key)
		if !ok {
			continue
		}
		return parsePathList(raw)
	}
	return nil
}

func parseTrustedFoldersEnv(keys ...string) []string {
	for _, key := range keys {
		raw, ok := os.LookupEnv(key)
		if !ok {
			continue
		}
		values := parsePathList(raw)
		normalized, valid := normalizeTrustedFolderList(values, "")
		if !valid {
			return nil
		}
		return normalized
	}
	return nil
}

func cloneStringSlice(input []string) []string {
	if input == nil {
		return nil
	}

	out := make([]string, len(input))
	copy(out, input)
	return out
}

func cloneStringSet(input map[string]struct{}) map[string]struct{} {
	if input == nil {
		return nil
	}

	out := make(map[string]struct{}, len(input))
	for value := range input {
		out[value] = struct{}{}
	}
	return out
}

func cloneBoolPtr(input *bool) *bool {
	if input == nil {
		return nil
	}
	value := *input
	return &value
}

func boolPtr(value bool) *bool {
	out := value
	return &out
}

func normalizeLanguageList(raw []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(raw))
	for _, value := range raw {
		language := strings.ToLower(strings.TrimSpace(value))
		if language == "" {
			continue
		}
		if _, ok := seen[language]; ok {
			continue
		}
		seen[language] = struct{}{}
		out = append(out, language)
	}
	return out
}

func loadGlobalConfigSnapshot(storagePath string) globalConfigSnapshot {
	configPath, ok := globalConfigPath(storagePath)
	if !ok {
		return globalConfigSnapshot{}
	}

	return loadConfigSnapshotFromPath(configPath, "")
}

func loadProjectConfigSnapshot(sourceRoot string) globalConfigSnapshot {
	root := strings.TrimSpace(sourceRoot)
	if root == "" {
		return globalConfigSnapshot{}
	}

	configPath := filepath.Join(root, projectConfigFileName)
	return loadConfigSnapshotFromPath(configPath, root)
}

func loadConfigSnapshotFromPath(configPath string, projectRoot string) globalConfigSnapshot {
	configPath = strings.TrimSpace(configPath)
	if configPath == "" {
		return globalConfigSnapshot{}
	}

	payload, err := os.ReadFile(configPath)
	if err != nil {
		return globalConfigSnapshot{}
	}

	text := strings.TrimPrefix(string(payload), "\uFEFF")
	clean := stripJSONCTrailingCommas(stripJSONCComments(text))

	var raw map[string]any
	if err := json.Unmarshal([]byte(clean), &raw); err != nil {
		return globalConfigSnapshot{}
	}

	snapshot := globalConfigSnapshot{}

	if value, exists := raw["languages"]; exists {
		parsed, ok := parseNullableStringList(value, normalizeLanguageList)
		if ok {
			snapshot.HasLanguages = true
			snapshot.Languages = parsed
		}
	}

	if value, exists := raw["freshness_mode"]; exists {
		mode, ok := value.(string)
		if ok {
			snapshot.HasFreshnessMode = true
			snapshot.FreshnessMode = mode
		}
	}

	if value, exists := raw["meta_fields"]; exists {
		parsed, ok := parseNullableStringList(value, trimAndFilterNonEmpty)
		if ok {
			snapshot.HasMetaFields = true
			snapshot.MetaFields = parsed
		}
	}

	if value, exists := raw["max_folder_files"]; exists {
		if parsed, ok := parsePositiveJSONNumber(value); ok {
			snapshot.HasMaxFolderFiles = true
			snapshot.MaxFolderFiles = parsed
		}
	}

	if value, exists := raw["max_index_files"]; exists {
		if parsed, ok := parsePositiveJSONNumber(value); ok {
			snapshot.HasMaxIndexFiles = true
			snapshot.MaxIndexFiles = parsed
		}
	}

	if value, exists := raw["extra_ignore_patterns"]; exists {
		parsed, ok := parseStringList(value, trimAndFilterNonEmpty)
		if ok {
			snapshot.HasExtraIgnorePatterns = true
			snapshot.ExtraIgnorePatterns = parsed
		}
	}

	if value, exists := raw["exclude_secret_patterns"]; exists {
		parsed, ok := parseStringList(value, trimAndFilterNonEmpty)
		if ok {
			snapshot.HasExcludeSecretPatterns = true
			snapshot.ExcludeSecretPatterns = parsed
		}
	}

	if value, exists := raw["trusted_folders"]; exists {
		parsed, ok := parseTrustedFoldersList(value, projectRoot)
		if !ok {
			return globalConfigSnapshot{}
		}
		snapshot.HasTrustedFolders = true
		snapshot.TrustedFolders = parsed
	}

	if value, exists := raw["trusted_folders_whitelist_mode"]; exists {
		if mode, ok := value.(bool); ok {
			snapshot.HasTrustedFoldersWhitelistMode = true
			snapshot.TrustedFoldersWhitelistMode = mode
		}
	}

	if value, exists := raw["disabled_tools"]; exists {
		parsed, ok := parseStringList(value, trimAndFilterNonEmpty)
		if ok {
			snapshot.HasDisabledTools = true
			snapshot.DisabledTools = parsed
		}
	}

	return snapshot
}

func globalConfigPath(storagePath string) (string, bool) {
	basePath := strings.TrimSpace(storagePath)
	if basePath == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", false
		}
		basePath = filepath.Join(home, defaultStorageDirName)
	}

	return filepath.Join(basePath, globalConfigFileName), true
}

func parseNullableStringList(
	value any,
	normalizer func([]string) []string,
) ([]string, bool) {
	if value == nil {
		return nil, true
	}
	return parseStringList(value, normalizer)
}

func parseStringList(
	value any,
	normalizer func([]string) []string,
) ([]string, bool) {
	items, ok := value.([]any)
	if !ok {
		return nil, false
	}

	parsed := make([]string, 0, len(items))
	for _, item := range items {
		text, ok := item.(string)
		if !ok {
			return nil, false
		}
		parsed = append(parsed, text)
	}
	return normalizer(parsed), true
}

func parsePositiveJSONNumber(value any) (int, bool) {
	switch typed := value.(type) {
	case float64:
		parsed := int(typed)
		if float64(parsed) != typed || parsed <= 0 {
			return 0, false
		}
		return parsed, true
	case int:
		if typed <= 0 {
			return 0, false
		}
		return typed, true
	case int32:
		if typed <= 0 {
			return 0, false
		}
		return int(typed), true
	case int64:
		if typed <= 0 {
			return 0, false
		}
		return int(typed), true
	default:
		return 0, false
	}
}

func parseTrustedFoldersList(value any, projectRoot string) ([]string, bool) {
	items, ok := parseStringList(value, trimAndFilterNonEmpty)
	if !ok {
		return nil, false
	}
	return normalizeTrustedFolderList(items, projectRoot)
}

func normalizeTrustedFolderList(values []string, projectRoot string) ([]string, bool) {
	root := strings.TrimSpace(projectRoot)
	if root != "" {
		var ok bool
		root, ok = normalizeAbsolutePath(root)
		if !ok {
			return nil, false
		}
	}

	seen := map[string]struct{}{}
	normalized := make([]string, 0, len(values))
	for _, raw := range values {
		entry := strings.TrimSpace(raw)
		if entry == "" {
			continue
		}

		resolved, ok := normalizeTrustedFolderPath(entry, root)
		if !ok {
			return nil, false
		}
		if _, exists := seen[resolved]; exists {
			continue
		}
		seen[resolved] = struct{}{}
		normalized = append(normalized, resolved)
	}
	return normalized, true
}

func normalizeTrustedFolderPath(raw, projectRoot string) (string, bool) {
	if projectRoot != "" {
		switch {
		case raw == "." || raw == "./":
			return projectRoot, true
		case strings.HasPrefix(raw, "./"):
			joined := filepath.Join(projectRoot, raw[2:])
			normalized, ok := normalizeAbsolutePath(joined)
			if !ok || !pathWithinRoot(projectRoot, normalized) {
				return "", false
			}
			return normalized, true
		case !filepath.IsAbs(raw) && !strings.HasPrefix(raw, "~"):
			joined := filepath.Join(projectRoot, raw)
			normalized, ok := normalizeAbsolutePath(joined)
			if !ok || !pathWithinRoot(projectRoot, normalized) {
				return "", false
			}
			return normalized, true
		}
	}

	if projectRoot == "" && !filepath.IsAbs(raw) && !strings.HasPrefix(raw, "~") {
		return "", false
	}

	normalized, ok := normalizeAbsolutePath(raw)
	if !ok {
		return "", false
	}
	return normalized, true
}

func normalizeAbsolutePath(raw string) (string, bool) {
	path := strings.TrimSpace(raw)
	if path == "" {
		return "", false
	}

	if path == "~" || strings.HasPrefix(path, "~/") || strings.HasPrefix(path, "~\\") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", false
		}
		if path == "~" {
			path = home
		} else {
			path = filepath.Join(home, path[2:])
		}
	}

	abs, err := filepath.Abs(path)
	if err != nil {
		return "", false
	}
	abs = filepath.Clean(abs)

	if evaluated, err := filepath.EvalSymlinks(abs); err == nil && strings.TrimSpace(evaluated) != "" {
		abs = filepath.Clean(evaluated)
	}
	return abs, true
}

func pathWithinRoot(root, candidate string) bool {
	rel, err := filepath.Rel(root, candidate)
	if err != nil {
		return false
	}
	if rel == "." {
		return true
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func trimAndFilterNonEmpty(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		out = append(out, trimmed)
	}
	return out
}

func stripJSONCComments(input string) string {
	out := strings.Builder{}
	out.Grow(len(input))

	inString := false
	escaped := false
	for i := 0; i < len(input); i++ {
		ch := input[i]

		if inString {
			out.WriteByte(ch)
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == '"' {
				inString = false
			}
			continue
		}

		if ch == '"' {
			inString = true
			out.WriteByte(ch)
			continue
		}

		if ch == '/' && i+1 < len(input) {
			next := input[i+1]
			if next == '/' {
				i += 2
				for i < len(input) && input[i] != '\n' {
					i++
				}
				if i < len(input) {
					out.WriteByte('\n')
				}
				continue
			}
			if next == '*' {
				i += 2
				for i+1 < len(input) && !(input[i] == '*' && input[i+1] == '/') {
					i++
				}
				if i+1 < len(input) {
					i++
				}
				continue
			}
		}

		out.WriteByte(ch)
	}

	return out.String()
}

func stripJSONCTrailingCommas(input string) string {
	out := strings.Builder{}
	out.Grow(len(input))

	inString := false
	escaped := false
	for i := 0; i < len(input); i++ {
		ch := input[i]

		if inString {
			out.WriteByte(ch)
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == '"' {
				inString = false
			}
			continue
		}

		if ch == '"' {
			inString = true
			out.WriteByte(ch)
			continue
		}

		if ch == ',' {
			next := nextNonWhitespaceByte(input, i+1)
			if next == '}' || next == ']' {
				continue
			}
		}

		out.WriteByte(ch)
	}

	return out.String()
}

func nextNonWhitespaceByte(input string, start int) byte {
	for i := start; i < len(input); i++ {
		switch input[i] {
		case ' ', '\t', '\n', '\r':
			continue
		default:
			return input[i]
		}
	}
	return 0
}
