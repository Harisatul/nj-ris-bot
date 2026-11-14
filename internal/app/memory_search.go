package app

import (
	"net/http"
	"strings"
)

func (a *App) memorySemanticSearch(w http.ResponseWriter, r *http.Request) {
	var d struct {
		Query         string   `json:"query"`
		Limit         int      `json:"limit"`
		MinSimilarity *float64 `json:"min_similarity"`
	}
	if !decode(w, r, &d) {
		return
	}
	d.Query = strings.TrimSpace(d.Query)
	if len(d.Query) < 2 || len(d.Query) > 1000 {
		jsonErr(w, 400, "query length must be 2..1000")
		return
	}
	if d.Limit == 0 {
		d.Limit = 5
	}
	if d.Limit < 1 || d.Limit > 20 {
		jsonErr(w, 400, "limit must be 1..20")
		return
	}
	minSimilarity := 0.2
	if d.MinSimilarity != nil {
		minSimilarity = *d.MinSimilarity
	}
	if minSimilarity < -1 || minSimilarity > 1 {
		jsonErr(w, 400, "min_similarity must be between -1 and 1")
		return
	}
	if a.Embed == nil {
		jsonErr(w, 503, "semantic embedding provider is not configured")
		return
	}
	vector, err := a.Embed.Embed(r.Context(), d.Query)
	if err != nil {
		jsonErr(w, 503, "semantic embedding provider unavailable")
		return
	}
	rows, err := a.DB.Query(r.Context(), `
SELECT m.id::text AS memory_id,m.title,left(m.summary,1200) AS snippet,m.category,m.importance,m.occurred_at,
       m.source_kind,m.source_ref,
       (1-(e.embedding <=> ($3::text)::vector))::float8 AS similarity
FROM mimo.semantic_embeddings e
JOIN mimo.search_chunks c ON c.id=e.chunk_id AND c.content_hash=e.content_hash
JOIN mimo.memory_entries m ON m.id=c.source_id
WHERE c.owner_id=$1 AND c.source_type='memory' AND e.model_id=$2
	  AND e.dimensions=$4 AND m.scope_type='personal' AND m.scope_id=$1
  AND m.status='active' AND (m.expires_at IS NULL OR m.expires_at>now())
  AND (1-(e.embedding <=> ($3::text)::vector)) >= $5
ORDER BY e.embedding <=> ($3::text)::vector
		LIMIT $6`, a.owner(r.Context()), a.C.EmbeddingModel, vectorText(vector), a.C.EmbeddingDimensions, minSimilarity, d.Limit)
	if err != nil {
		internalErr(w, err)
		return
	}
	defer rows.Close()
	type result struct {
		MemoryID   string  `json:"memory_id"`
		Title      string  `json:"title"`
		Snippet    string  `json:"snippet"`
		Category   string  `json:"category"`
		Importance int     `json:"importance"`
		OccurredAt any     `json:"occurred_at"`
		SourceKind *string `json:"source_kind,omitempty"`
		SourceRef  *string `json:"source_ref,omitempty"`
		Similarity float64 `json:"similarity"`
	}
	results := make([]result, 0, d.Limit)
	for rows.Next() {
		var item result
		if err = rows.Scan(&item.MemoryID, &item.Title, &item.Snippet, &item.Category, &item.Importance, &item.OccurredAt, &item.SourceKind, &item.SourceRef, &item.Similarity); err != nil {
			internalErr(w, err)
			return
		}
		results = append(results, item)
	}
	if err = rows.Err(); err != nil {
		internalErr(w, err)
		return
	}
	jsonOK(w, map[string]any{"query": d.Query, "model": a.C.EmbeddingModel, "results": results})
}
