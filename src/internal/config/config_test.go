package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLoadFanoutDefaults(t *testing.T) {
	isolateConfigPath(t)

	t.Setenv("GOCODEMUNCH_FANOUT_MODE", "")
	t.Setenv("GOCODEMUNCH_FANOUT_OVERLOAD_POLICY", "")
	t.Setenv("GOCODEMUNCH_FANOUT_MAX_WORKERS", "")
	t.Setenv("GOCODEMUNCH_FANOUT_MAX_QUEUE_DEPTH", "")
	t.Setenv("GOCODEMUNCH_REQUEST_TIMEOUT_MS", "")
	t.Setenv("GOCODEMUNCH_FANOUT_ITEM_TIMEOUT_MS", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.FanoutMode != "serial" {
		t.Fatalf("expected default fanout mode serial, got %q", cfg.FanoutMode)
	}
	if cfg.FanoutOverloadPolicy != "reject" {
		t.Fatalf("expected default overload policy reject, got %q", cfg.FanoutOverloadPolicy)
	}
	if cfg.FanoutMaxWorkers != 4 {
		t.Fatalf("expected default fanout max workers 4, got %d", cfg.FanoutMaxWorkers)
	}
	if cfg.FanoutMaxQueueDepth != 256 {
		t.Fatalf("expected default fanout queue depth 256, got %d", cfg.FanoutMaxQueueDepth)
	}
	if cfg.RequestTimeoutMS != 0 {
		t.Fatalf("expected default request timeout 0, got %d", cfg.RequestTimeoutMS)
	}
	if cfg.FanoutItemTimeoutMS != 0 {
		t.Fatalf("expected default fanout item timeout 0, got %d", cfg.FanoutItemTimeoutMS)
	}
}

func TestLoadFanoutConfigFromEnvironment(t *testing.T) {
	isolateConfigPath(t)

	t.Setenv("GOCODEMUNCH_FANOUT_MODE", "parallel")
	t.Setenv("GOCODEMUNCH_FANOUT_OVERLOAD_POLICY", "degrade")
	t.Setenv("GOCODEMUNCH_FANOUT_MAX_WORKERS", "9")
	t.Setenv("GOCODEMUNCH_FANOUT_MAX_QUEUE_DEPTH", "17")
	t.Setenv("GOCODEMUNCH_REQUEST_TIMEOUT_MS", "2500")
	t.Setenv("GOCODEMUNCH_FANOUT_ITEM_TIMEOUT_MS", "600")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.FanoutMode != "parallel" {
		t.Fatalf("expected parallel fanout mode, got %q", cfg.FanoutMode)
	}
	if cfg.FanoutOverloadPolicy != "degrade" {
		t.Fatalf("expected overload policy degrade, got %q", cfg.FanoutOverloadPolicy)
	}
	if cfg.FanoutMaxWorkers != 9 {
		t.Fatalf("expected fanout max workers 9, got %d", cfg.FanoutMaxWorkers)
	}
	if cfg.FanoutMaxQueueDepth != 17 {
		t.Fatalf("expected fanout queue depth 17, got %d", cfg.FanoutMaxQueueDepth)
	}
	if cfg.RequestTimeoutMS != 2500 {
		t.Fatalf("expected request timeout 2500, got %d", cfg.RequestTimeoutMS)
	}
	if cfg.FanoutItemTimeoutMS != 600 {
		t.Fatalf("expected fanout item timeout 600, got %d", cfg.FanoutItemTimeoutMS)
	}
}

func TestLoadFanoutInvalidOverloadPolicyFallsBackToReject(t *testing.T) {
	isolateConfigPath(t)

	t.Setenv("GOCODEMUNCH_FANOUT_OVERLOAD_POLICY", "burst")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.FanoutOverloadPolicy != "reject" {
		t.Fatalf("expected invalid overload policy to fall back to reject, got %q", cfg.FanoutOverloadPolicy)
	}
}

func TestVectorConfigDefaults(t *testing.T) {
	isolateConfigPath(t)

	t.Setenv("VECTOR_BACKEND", "")
	t.Setenv("VECTOR_TOP_K", "")
	t.Setenv("VECTOR_QUERY_TIMEOUT_MS", "")
	t.Setenv("EMBEDDING_PROVIDER", "")
	t.Setenv("EMBEDDING_MODEL", "")
	t.Setenv("OLLAMA_BASE_URL", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.VectorBackend != defaultVectorBackend {
		t.Fatalf("expected default vector backend %q, got %q", defaultVectorBackend, cfg.VectorBackend)
	}
	if cfg.VectorTopK != defaultVectorTopK {
		t.Fatalf("expected default vector top-k %d, got %d", defaultVectorTopK, cfg.VectorTopK)
	}
	if cfg.VectorQueryTimeoutMS != defaultVectorQueryTimeoutMS {
		t.Fatalf(
			"expected default vector query timeout %d, got %d",
			defaultVectorQueryTimeoutMS,
			cfg.VectorQueryTimeoutMS,
		)
	}
	if cfg.EmbeddingProvider != defaultEmbeddingProvider {
		t.Fatalf(
			"expected default embedding provider %q, got %q",
			defaultEmbeddingProvider,
			cfg.EmbeddingProvider,
		)
	}
	if cfg.EmbeddingModel != defaultEmbeddingModel {
		t.Fatalf("expected default embedding model %q, got %q", defaultEmbeddingModel, cfg.EmbeddingModel)
	}
	if cfg.OllamaBaseURL != defaultOllamaBaseURL {
		t.Fatalf("expected default ollama base URL %q, got %q", defaultOllamaBaseURL, cfg.OllamaBaseURL)
	}
}

func TestVectorConfigEnvOverrides(t *testing.T) {
	isolateConfigPath(t)

	t.Setenv("VECTOR_BACKEND", "SQLITE")
	t.Setenv("VECTOR_TOP_K", "11")
	t.Setenv("VECTOR_QUERY_TIMEOUT_MS", "1234")
	t.Setenv("EMBEDDING_PROVIDER", "OLLAMA")
	t.Setenv("EMBEDDING_MODEL", "custom-bge")
	t.Setenv("OLLAMA_BASE_URL", "http://localhost:11434")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.VectorBackend != "sqlite" {
		t.Fatalf("expected vector backend sqlite, got %q", cfg.VectorBackend)
	}
	if cfg.VectorTopK != 11 {
		t.Fatalf("expected vector top-k 11, got %d", cfg.VectorTopK)
	}
	if cfg.VectorQueryTimeoutMS != 1234 {
		t.Fatalf("expected vector query timeout 1234, got %d", cfg.VectorQueryTimeoutMS)
	}
	if cfg.EmbeddingProvider != "ollama" {
		t.Fatalf("expected embedding provider ollama, got %q", cfg.EmbeddingProvider)
	}
	if cfg.EmbeddingModel != "custom-bge" {
		t.Fatalf("expected embedding model custom-bge, got %q", cfg.EmbeddingModel)
	}
	if cfg.OllamaBaseURL != "http://localhost:11434" {
		t.Fatalf("expected ollama base URL override, got %q", cfg.OllamaBaseURL)
	}
}

func TestVectorConfigVLLMProviderSwitching(t *testing.T) {
	isolateConfigPath(t)

	t.Setenv("EMBEDDING_PROVIDER", "VLLM")
	t.Setenv("VLLM_BASE_URL", "https://vllm.internal/v1")
	t.Setenv("VLLM_MODEL", "nomic-embed-text")
	t.Setenv("VLLM_API_KEY", "test-vllm-key")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config with vllm provider: %v", err)
	}
	if cfg.EmbeddingProvider != "vllm" {
		t.Fatalf("expected embedding provider vllm, got %q", cfg.EmbeddingProvider)
	}
	if cfg.VLLMBaseURL != "https://vllm.internal/v1" {
		t.Fatalf("expected vllm base URL override, got %q", cfg.VLLMBaseURL)
	}
	if cfg.VLLMModel != "nomic-embed-text" {
		t.Fatalf("expected vllm model override, got %q", cfg.VLLMModel)
	}
	if cfg.VLLMAPIKey != "test-vllm-key" {
		t.Fatalf("expected vllm API key override, got %q", cfg.VLLMAPIKey)
	}

	t.Setenv("EMBEDDING_PROVIDER", "ollama")
	t.Setenv("VLLM_BASE_URL", "not-a-url")

	cfg, err = Load()
	if err != nil {
		t.Fatalf("expected provider switch back to ollama to succeed: %v", err)
	}
	if cfg.EmbeddingProvider != "ollama" {
		t.Fatalf("expected embedding provider ollama after switch, got %q", cfg.EmbeddingProvider)
	}
}

func TestVectorConfigVLLMInvalidProviderError(t *testing.T) {
	isolateConfigPath(t)

	t.Setenv("EMBEDDING_PROVIDER", "openai")

	_, err := Load()
	if err == nil {
		t.Fatal("expected validation error for invalid embedding provider")
	}

	message := err.Error()
	if !strings.Contains(message, "EMBEDDING_PROVIDER") {
		t.Fatalf("expected validation message to mention EMBEDDING_PROVIDER: %q", message)
	}
	if !strings.Contains(message, `["ollama", "vllm"]`) {
		t.Fatalf("expected validation message to mention allowed providers: %q", message)
	}
}

func TestQdrantConfigDefaults(t *testing.T) {
	isolateConfigPath(t)

	t.Setenv("VECTOR_BACKEND", "")
	t.Setenv("QDRANT_URL", "")
	t.Setenv("QDRANT_API_KEY", "")
	t.Setenv("QDRANT_COLLECTION", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.QdrantURL != defaultQdrantURL {
		t.Fatalf("expected default qdrant URL %q, got %q", defaultQdrantURL, cfg.QdrantURL)
	}
	if cfg.QdrantAPIKey != "" {
		t.Fatalf("expected default qdrant API key to be empty, got %q", cfg.QdrantAPIKey)
	}
	if cfg.QdrantCollection != defaultQdrantCollection {
		t.Fatalf(
			"expected default qdrant collection %q, got %q",
			defaultQdrantCollection,
			cfg.QdrantCollection,
		)
	}
}

func TestQdrantConfigEnvOverrides(t *testing.T) {
	isolateConfigPath(t)

	t.Setenv("VECTOR_BACKEND", "QDRANT")
	t.Setenv("QDRANT_URL", "https://qdrant.internal:6333")
	t.Setenv("QDRANT_API_KEY", "test-token")
	t.Setenv("QDRANT_COLLECTION", "team-vectors")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.VectorBackend != "qdrant" {
		t.Fatalf("expected vector backend qdrant, got %q", cfg.VectorBackend)
	}
	if cfg.QdrantURL != "https://qdrant.internal:6333" {
		t.Fatalf("expected qdrant URL override, got %q", cfg.QdrantURL)
	}
	if cfg.QdrantAPIKey != "test-token" {
		t.Fatalf("expected qdrant API key override, got %q", cfg.QdrantAPIKey)
	}
	if cfg.QdrantCollection != "team-vectors" {
		t.Fatalf("expected qdrant collection override, got %q", cfg.QdrantCollection)
	}
}

func TestQdrantConfigValidationErrors(t *testing.T) {
	t.Setenv("VECTOR_BACKEND", "qdrant")
	t.Setenv("QDRANT_URL", "")
	t.Setenv("QDRANT_COLLECTION", "")

	err := applyVectorEnvOverrides(&Config{})
	if err == nil {
		t.Fatal("expected validation error for missing qdrant configuration")
	}

	message := err.Error()
	for _, field := range []string{"QDRANT_URL", "QDRANT_COLLECTION"} {
		if !strings.Contains(message, field) {
			t.Fatalf("expected validation message to mention %s: %q", field, message)
		}
	}
}

func TestLoadVectorEnvironmentValidationErrors(t *testing.T) {
	isolateConfigPath(t)

	t.Setenv("VECTOR_BACKEND", "redis")
	t.Setenv("VECTOR_TOP_K", "0")
	t.Setenv("VECTOR_QUERY_TIMEOUT_MS", "-2")
	t.Setenv("EMBEDDING_PROVIDER", "unknown")
	t.Setenv("OLLAMA_BASE_URL", "localhost:11434")

	_, err := Load()
	if err == nil {
		t.Fatal("expected validation error for invalid vector env configuration")
	}

	message := err.Error()
	for _, field := range []string{
		"VECTOR_BACKEND",
		"VECTOR_TOP_K",
		"VECTOR_QUERY_TIMEOUT_MS",
		"EMBEDDING_PROVIDER",
		"OLLAMA_BASE_URL",
	} {
		if !strings.Contains(message, field) {
			t.Fatalf("expected validation message to mention %s: %q", field, message)
		}
	}
}

func TestLoadLanguagesFromEnvironmentCSV(t *testing.T) {
	isolateConfigPath(t)

	t.Setenv("GOCODEMUNCH_LANGUAGES", "python, sql,GO,python")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	expected := []string{"python", "sql", "go"}
	if !reflect.DeepEqual(cfg.Languages, expected) {
		t.Fatalf("expected normalized language list %#v, got %#v", expected, cfg.Languages)
	}
}

func TestLoadLanguagesFromEnvironmentJSON(t *testing.T) {
	isolateConfigPath(t)

	t.Setenv("GOCODEMUNCH_LANGUAGES", `["Python","sql","PYTHON"]`)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	expected := []string{"python", "sql"}
	if !reflect.DeepEqual(cfg.Languages, expected) {
		t.Fatalf("expected normalized language list %#v, got %#v", expected, cfg.Languages)
	}
}

func TestLoadLanguagesFromConfigFileJSONC(t *testing.T) {
	storagePath := isolateConfigPath(t)
	t.Setenv("GOCODEMUNCH_LANGUAGES", "")
	t.Setenv("JCODEMUNCH_LANGUAGES", "")

	configPayload := `{
		// language gating for parity
		"languages": ["Python", "sql", "PYTHON",],
	}`
	writeConfigFile(t, storagePath, configPayload)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	expected := []string{"python", "sql"}
	if !reflect.DeepEqual(cfg.Languages, expected) {
		t.Fatalf("expected languages from config file %#v, got %#v", expected, cfg.Languages)
	}
}

func TestLoadConfigLanguagesTakePrecedenceOverEnvironment(t *testing.T) {
	storagePath := isolateConfigPath(t)
	t.Setenv("GOCODEMUNCH_LANGUAGES", "python,sql")

	writeConfigFile(t, storagePath, `{"languages": ["go"]}`)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	expected := []string{"go"}
	if !reflect.DeepEqual(cfg.Languages, expected) {
		t.Fatalf("expected config-file languages to override env: got %#v want %#v", cfg.Languages, expected)
	}
}

func TestLoadInvalidConfigLanguagesFallsBackToEnvironment(t *testing.T) {
	storagePath := isolateConfigPath(t)
	t.Setenv("GOCODEMUNCH_LANGUAGES", "python,sql")

	writeConfigFile(t, storagePath, `{"languages": "python"}`)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	expected := []string{"python", "sql"}
	if !reflect.DeepEqual(cfg.Languages, expected) {
		t.Fatalf("expected env fallback when config languages is invalid: got %#v want %#v", cfg.Languages, expected)
	}
}

func TestLoadDisabledToolsFromConfigFileTakePrecedence(t *testing.T) {
	storagePath := isolateConfigPath(t)
	t.Setenv("GOCODEMUNCH_DISABLED_TOOLS", "index_repo")

	writeConfigFile(t, storagePath, `{"disabled_tools": ["search_text"]}`)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if !cfg.IsToolDisabled("search_text") {
		t.Fatalf("expected search_text to be disabled via config file")
	}
	if cfg.IsToolDisabled("index_repo") {
		t.Fatalf("expected env disabled tools to be ignored when config file sets disabled_tools")
	}
}

func TestLoadMetaFieldsNullInConfigSkipsEnvFallback(t *testing.T) {
	storagePath := isolateConfigPath(t)
	t.Setenv("GOCODEMUNCH_META_FIELDS", "none")

	writeConfigFile(t, storagePath, `{"meta_fields": null}`)

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if cfg.MetaFields != nil {
		t.Fatalf("expected nil meta fields from explicit config null, got %#v", cfg.MetaFields)
	}
}

func TestIsToolDisabledSearchColumnsByLanguageGate(t *testing.T) {
	isolateConfigPath(t)

	cfg := Config{
		Languages: []string{"python", "go"},
		Disabled:  map[string]struct{}{},
	}
	if !cfg.IsToolDisabled("search_columns") {
		t.Fatalf("expected search_columns to be disabled when sql is not enabled")
	}

	cfg = Config{
		Languages: []string{"python", "sql"},
		Disabled:  map[string]struct{}{},
	}
	if cfg.IsToolDisabled("search_columns") {
		t.Fatalf("expected search_columns to stay enabled when sql is enabled")
	}
}

func TestForProjectRootOverridesGlobalConfigValues(t *testing.T) {
	isolateConfigPath(t)
	sourceRoot := t.TempDir()

	base := Config{
		Languages:             []string{"python"},
		FreshnessMode:         "strict",
		MetaFields:            []string{"index_stale"},
		MaxFolderFiles:        2000,
		MaxIndexFiles:         10000,
		ExtraIgnorePatterns:   []string{"vendor/"},
		ExcludeSecretPatterns: []string{"*token*"},
		Disabled: map[string]struct{}{
			"search_text": {},
		},
	}

	writeProjectConfigFile(t, sourceRoot, `{
		"languages": ["go"],
		"freshness_mode": "relaxed",
		"meta_fields": null,
		"max_folder_files": 321,
		"max_index_files": 654,
		"extra_ignore_patterns": ["build/"],
		"exclude_secret_patterns": ["*secret*"],
		"disabled_tools": ["get_file_tree"]
	}`)

	merged := base.ForProjectRoot(sourceRoot)

	if !reflect.DeepEqual(merged.Languages, []string{"go"}) {
		t.Fatalf("expected project languages override, got %#v", merged.Languages)
	}
	if merged.FreshnessMode != "relaxed" {
		t.Fatalf("expected project freshness_mode override, got %q", merged.FreshnessMode)
	}
	if merged.MetaFields != nil {
		t.Fatalf("expected project meta_fields null override, got %#v", merged.MetaFields)
	}
	if merged.MaxFolderFiles != 321 {
		t.Fatalf("expected project max_folder_files override, got %d", merged.MaxFolderFiles)
	}
	if merged.MaxIndexFiles != 654 {
		t.Fatalf("expected project max_index_files override, got %d", merged.MaxIndexFiles)
	}
	if !reflect.DeepEqual(merged.ExtraIgnorePatterns, []string{"build/"}) {
		t.Fatalf(
			"expected project extra_ignore_patterns override, got %#v",
			merged.ExtraIgnorePatterns,
		)
	}
	if !reflect.DeepEqual(merged.ExcludeSecretPatterns, []string{"*secret*"}) {
		t.Fatalf(
			"expected project exclude_secret_patterns override, got %#v",
			merged.ExcludeSecretPatterns,
		)
	}
	if _, ok := merged.Disabled["get_file_tree"]; !ok {
		t.Fatalf("expected get_file_tree disabled via project config: %#v", merged.Disabled)
	}
	if _, ok := merged.Disabled["search_text"]; ok {
		t.Fatalf("expected project disabled_tools to replace global disabled map: %#v", merged.Disabled)
	}
	if _, ok := base.Disabled["search_text"]; !ok {
		t.Fatalf("expected base config to remain unchanged, got %#v", base.Disabled)
	}
	if base.MaxFolderFiles != 2000 || base.MaxIndexFiles != 10000 {
		t.Fatalf("expected base max file caps to remain unchanged, got %#v", base)
	}
	if !reflect.DeepEqual(base.ExtraIgnorePatterns, []string{"vendor/"}) {
		t.Fatalf(
			"expected base extra_ignore_patterns to remain unchanged, got %#v",
			base.ExtraIgnorePatterns,
		)
	}
	if !reflect.DeepEqual(base.ExcludeSecretPatterns, []string{"*token*"}) {
		t.Fatalf(
			"expected base exclude_secret_patterns to remain unchanged, got %#v",
			base.ExcludeSecretPatterns,
		)
	}
}

func TestForProjectRootInvalidProjectValuesDoNotOverrideBase(t *testing.T) {
	isolateConfigPath(t)
	sourceRoot := t.TempDir()

	base := Config{
		Languages:             []string{"python"},
		FreshnessMode:         "strict",
		MetaFields:            []string{"index_stale"},
		MaxFolderFiles:        2000,
		MaxIndexFiles:         10000,
		ExtraIgnorePatterns:   []string{"vendor/"},
		ExcludeSecretPatterns: []string{"*token*"},
		Disabled: map[string]struct{}{
			"search_text": {},
		},
	}

	writeProjectConfigFile(t, sourceRoot, `{
		"languages": "go",
		"freshness_mode": ["relaxed"],
		"meta_fields": "none",
		"max_folder_files": "many",
		"max_index_files": false,
		"extra_ignore_patterns": "build/",
		"exclude_secret_patterns": 123,
		"disabled_tools": "get_file_tree"
	}`)

	merged := base.ForProjectRoot(sourceRoot)

	if !reflect.DeepEqual(merged.Languages, base.Languages) {
		t.Fatalf("expected base languages to remain, got %#v want %#v", merged.Languages, base.Languages)
	}
	if merged.FreshnessMode != base.FreshnessMode {
		t.Fatalf("expected base freshness_mode to remain, got %q want %q", merged.FreshnessMode, base.FreshnessMode)
	}
	if !reflect.DeepEqual(merged.MetaFields, base.MetaFields) {
		t.Fatalf("expected base meta_fields to remain, got %#v want %#v", merged.MetaFields, base.MetaFields)
	}
	if merged.MaxFolderFiles != base.MaxFolderFiles {
		t.Fatalf(
			"expected base max_folder_files to remain, got %d want %d",
			merged.MaxFolderFiles,
			base.MaxFolderFiles,
		)
	}
	if merged.MaxIndexFiles != base.MaxIndexFiles {
		t.Fatalf(
			"expected base max_index_files to remain, got %d want %d",
			merged.MaxIndexFiles,
			base.MaxIndexFiles,
		)
	}
	if !reflect.DeepEqual(merged.ExtraIgnorePatterns, base.ExtraIgnorePatterns) {
		t.Fatalf(
			"expected base extra_ignore_patterns to remain, got %#v want %#v",
			merged.ExtraIgnorePatterns,
			base.ExtraIgnorePatterns,
		)
	}
	if !reflect.DeepEqual(merged.ExcludeSecretPatterns, base.ExcludeSecretPatterns) {
		t.Fatalf(
			"expected base exclude_secret_patterns to remain, got %#v want %#v",
			merged.ExcludeSecretPatterns,
			base.ExcludeSecretPatterns,
		)
	}
	if !reflect.DeepEqual(merged.Disabled, base.Disabled) {
		t.Fatalf("expected base disabled map to remain, got %#v want %#v", merged.Disabled, base.Disabled)
	}
}

func TestTrustedFoldersWhitelistEnabledDefaultsToTrue(t *testing.T) {
	var cfg Config
	if !cfg.TrustedFoldersWhitelistEnabled() {
		t.Fatalf("expected default whitelist mode=true when unset")
	}

	falseValue := false
	cfg.TrustedFoldersWhitelistMode = &falseValue
	if cfg.TrustedFoldersWhitelistEnabled() {
		t.Fatalf("expected whitelist mode=false when explicitly configured")
	}
}

func TestForProjectRootTrustedFoldersExpandAndOverride(t *testing.T) {
	isolateConfigPath(t)
	sourceRoot := t.TempDir()
	external := t.TempDir()

	base := Config{
		TrustedFolders: []string{"/global/trusted"},
	}

	writeProjectConfigFile(t, sourceRoot, fmt.Sprintf(`{
		"trusted_folders": [".", "./src", "src", %q],
		"trusted_folders_whitelist_mode": false
	}`, external))

	merged := base.ForProjectRoot(sourceRoot)
	canonicalRoot := canonicalPathForTest(t, sourceRoot)
	expected := []string{
		canonicalRoot,
		filepath.Clean(filepath.Join(canonicalRoot, "src")),
		canonicalPathForTest(t, external),
	}
	if !reflect.DeepEqual(merged.TrustedFolders, expected) {
		t.Fatalf("expected project trusted folders override %#v, got %#v", expected, merged.TrustedFolders)
	}
	if merged.TrustedFoldersWhitelistMode == nil || *merged.TrustedFoldersWhitelistMode {
		t.Fatalf(
			"expected project trusted_folders_whitelist_mode=false override, got %#v",
			merged.TrustedFoldersWhitelistMode,
		)
	}
	if !reflect.DeepEqual(base.TrustedFolders, []string{"/global/trusted"}) {
		t.Fatalf("expected base trusted folders to remain unchanged, got %#v", base.TrustedFolders)
	}
}

func TestForProjectRootTrustedFoldersEscapeDoesNotOverrideBase(t *testing.T) {
	isolateConfigPath(t)
	sourceRoot := t.TempDir()
	trueValue := true

	base := Config{
		TrustedFolders:              []string{filepath.Clean(sourceRoot)},
		TrustedFoldersWhitelistMode: &trueValue,
	}

	writeProjectConfigFile(t, sourceRoot, `{
		"trusted_folders": ["../outside"],
		"trusted_folders_whitelist_mode": false
	}`)

	merged := base.ForProjectRoot(sourceRoot)
	if !reflect.DeepEqual(merged.TrustedFolders, base.TrustedFolders) {
		t.Fatalf(
			"expected invalid trusted_folders escape to preserve base trusted folders: got %#v want %#v",
			merged.TrustedFolders,
			base.TrustedFolders,
		)
	}
	if merged.TrustedFoldersWhitelistMode == nil || !*merged.TrustedFoldersWhitelistMode {
		t.Fatalf(
			"expected invalid project trusted_folders to preserve base whitelist mode, got %#v",
			merged.TrustedFoldersWhitelistMode,
		)
	}
}

func TestLoadTrustedFoldersConfigTakesPrecedenceOverEnvironment(t *testing.T) {
	storagePath := isolateConfigPath(t)
	envTrusted := t.TempDir()
	fileTrusted := t.TempDir()
	t.Setenv("GOCODEMUNCH_TRUSTED_FOLDERS", envTrusted)
	t.Setenv("GOCODEMUNCH_TRUSTED_FOLDERS_WHITELIST_MODE", "true")

	writeConfigFile(t, storagePath, fmt.Sprintf(`{
		"trusted_folders": [%q],
		"trusted_folders_whitelist_mode": false
	}`, fileTrusted))

	cfg, err := Load()
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	if !reflect.DeepEqual(cfg.TrustedFolders, []string{canonicalPathForTest(t, fileTrusted)}) {
		t.Fatalf("expected trusted_folders from config file, got %#v", cfg.TrustedFolders)
	}
	if cfg.TrustedFoldersWhitelistMode == nil || *cfg.TrustedFoldersWhitelistMode {
		t.Fatalf(
			"expected trusted_folders_whitelist_mode=false from config file, got %#v",
			cfg.TrustedFoldersWhitelistMode,
		)
	}
}

func writeConfigFile(t *testing.T, storagePath, content string) {
	t.Helper()

	path := filepath.Join(storagePath, "config.jsonc")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write config file: %v", err)
	}
}

func writeProjectConfigFile(t *testing.T, sourceRoot, content string) {
	t.Helper()

	path := filepath.Join(sourceRoot, ".jcodemunch.jsonc")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write project config file: %v", err)
	}
}

func canonicalPathForTest(t *testing.T, raw string) string {
	t.Helper()

	path := filepath.Clean(raw)
	if evaluated, err := filepath.EvalSymlinks(path); err == nil && evaluated != "" {
		return filepath.Clean(evaluated)
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	return filepath.Clean(abs)
}

func isolateConfigPath(t *testing.T) string {
	t.Helper()

	storagePath := t.TempDir()
	t.Setenv("CODE_INDEX_PATH", storagePath)
	return storagePath
}
