package plaintext

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"aleutian-ai/ragctl/internal/domain"
	"aleutian-ai/ragctl/internal/normalize"
)

// Normalize reads src's materialized file and produces a single
// KnowledgeObject with trimmed raw content — no structure extraction.
func (n *Normalizer) Normalize(ctx context.Context, src domain.SourceSnapshot) ([]domain.KnowledgeObject, error) {
	raw, err := os.ReadFile(src.LocalPath)
	if err != nil {
		return nil, fmt.Errorf("plaintext: read %s: %w", src.LocalPath, err)
	}
	raw = normalize.NormalizeLineEndings(raw)

	content := strings.TrimSpace(string(raw))
	metadata := map[string]string{}
	if !utf8.ValidString(content) {
		content = strings.ToValidUTF8(content, "�")
		metadata["invalid_utf8_replaced"] = "true"
	}

	obj := domain.KnowledgeObject{
		SourceID:    src.SourceID,
		SourceURI:   src.URI,
		ContentType: "text",
		LogicalPath: src.LogicalPath,
		Title:       filepath.Base(src.LogicalPath),
		Version:     src.Version,
		Commit:      src.Commit,
		Content:     []byte(content),
		Metadata:    metadata,
	}
	return []domain.KnowledgeObject{obj}, nil
}
