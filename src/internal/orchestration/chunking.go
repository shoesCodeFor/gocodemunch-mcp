package orchestration

import (
	"crypto/sha1"
	"encoding/hex"
	"path"
	"sort"
	"strconv"
	"strings"

	"github.com/jgravelle/gocodemunch-mcp/src/internal/domain/indexing"
)

const (
	defaultChunkMaxLines      = 80
	defaultChunkMaxCharacters = 3000
)

type indexedFileContent struct {
	Repo     string
	Path     string
	Language string
	Content  []byte
	Fields   map[string]any
}

type normalizedChunkFile struct {
	repo     string
	path     string
	language string
	content  string
	fields   map[string]any
}

func buildDeterministicChunkMetadata(files []indexedFileContent) []indexing.VectorMetadata {
	return buildDeterministicChunkMetadataWithLimits(
		files,
		defaultChunkMaxLines,
		defaultChunkMaxCharacters,
	)
}

func buildDeterministicChunkMetadataWithLimits(
	files []indexedFileContent,
	maxChunkLines int,
	maxChunkCharacters int,
) []indexing.VectorMetadata {
	if maxChunkLines <= 0 {
		maxChunkLines = defaultChunkMaxLines
	}
	if maxChunkCharacters <= 0 {
		maxChunkCharacters = defaultChunkMaxCharacters
	}

	normalized := normalizeChunkInputFiles(files)
	if len(normalized) == 0 {
		return []indexing.VectorMetadata{}
	}

	chunks := make([]indexing.VectorMetadata, 0, len(normalized))
	for _, file := range normalized {
		lines := splitContentLines(file.content)
		if len(lines) == 0 {
			continue
		}

		for start := 0; start < len(lines); {
			end := endChunkWindow(lines, start, maxChunkLines, maxChunkCharacters)
			chunkText := strings.Join(lines[start:end], "\n")
			if strings.TrimSpace(chunkText) != "" {
				startLine := start + 1
				endLine := end
				chunks = append(chunks, indexing.VectorMetadata{
					Repo:      file.repo,
					Path:      file.path,
					Language:  file.language,
					ChunkID:   deterministicChunkID(file.repo, file.path, startLine, endLine, chunkText),
					ChunkText: chunkText,
					StartLine: startLine,
					EndLine:   endLine,
					Fields:    cloneChunkFields(file.fields),
				})
			}
			start = end
		}
	}

	return chunks
}

func normalizeChunkInputFiles(files []indexedFileContent) []normalizedChunkFile {
	normalized := make([]normalizedChunkFile, 0, len(files))
	for _, file := range files {
		filePath := normalizeChunkPath(file.Path)
		if filePath == "" {
			continue
		}

		language := strings.ToLower(strings.TrimSpace(file.Language))
		if language == "" {
			if detectedLanguage, ok := classifyLanguage(filePath); ok {
				language = detectedLanguage
			}
		}

		normalized = append(normalized, normalizedChunkFile{
			repo:     strings.TrimSpace(file.Repo),
			path:     filePath,
			language: language,
			content:  string(file.Content),
			fields:   cloneChunkFields(file.Fields),
		})
	}

	sort.SliceStable(normalized, func(i, j int) bool {
		if normalized[i].repo != normalized[j].repo {
			return normalized[i].repo < normalized[j].repo
		}
		return normalized[i].path < normalized[j].path
	})

	return normalized
}

func normalizeChunkPath(rawPath string) string {
	trimmed := strings.TrimSpace(strings.ReplaceAll(rawPath, "\\", "/"))
	trimmed = strings.TrimPrefix(trimmed, "./")
	trimmed = strings.Trim(trimmed, "/")
	if trimmed == "" {
		return ""
	}

	cleaned := path.Clean(trimmed)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return ""
	}
	if strings.Contains(cleaned, "/../") {
		return ""
	}
	return cleaned
}

func endChunkWindow(lines []string, start, maxChunkLines, maxChunkCharacters int) int {
	end := start
	currentCharacters := 0

	for end < len(lines) {
		nextCharacters := currentCharacters + len(lines[end])
		if end > start {
			nextCharacters++
		}

		nextLineCount := end - start + 1
		if end > start && (nextLineCount > maxChunkLines || nextCharacters > maxChunkCharacters) {
			break
		}

		currentCharacters = nextCharacters
		end++

		if nextLineCount >= maxChunkLines || currentCharacters >= maxChunkCharacters {
			break
		}
	}

	if end <= start {
		return start + 1
	}
	return end
}

func deterministicChunkID(repo, filePath string, startLine, endLine int, chunkText string) string {
	hasher := sha1.New()
	_, _ = hasher.Write([]byte(strings.TrimSpace(repo)))
	_, _ = hasher.Write([]byte{0})
	_, _ = hasher.Write([]byte(strings.TrimSpace(filePath)))
	_, _ = hasher.Write([]byte{0})
	_, _ = hasher.Write([]byte(strconv.Itoa(startLine)))
	_, _ = hasher.Write([]byte{0})
	_, _ = hasher.Write([]byte(strconv.Itoa(endLine)))
	_, _ = hasher.Write([]byte{0})
	_, _ = hasher.Write([]byte(chunkText))
	return hex.EncodeToString(hasher.Sum(nil))
}

func cloneChunkFields(fields map[string]any) map[string]any {
	if len(fields) == 0 {
		return map[string]any{}
	}

	cloned := make(map[string]any, len(fields))
	for key, value := range fields {
		cloned[key] = value
	}
	return cloned
}
