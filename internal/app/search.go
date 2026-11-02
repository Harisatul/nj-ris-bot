package app

import (
	"log"
	"math"
	"net/http"
	"sort"
	"strings"
)

type searchHit struct {
	SourceType string  `json:"source_type"`
	SourceID   string  `json:"source_id"`
	Snippet    string  `json:"snippet"`
	Score      float64 `json:"score"`
	Lexical    bool    `json:"lexical"`
	Semantic   bool    `json:"semantic"`
}

func (a *App) search(w http.ResponseWriter, r *http.Request) {
	var d struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if !decode(w, r, &d) {
		return
	}
	d.Query = strings.TrimSpace(d.Query)
	if len(d.Query) < 2 || len(d.Query) > 1000 {
		jsonErr(w, 400, "query length must be 2..1000")
		return
	}
	if d.Limit <= 0 {
		d.Limit = 10
	}
	if d.Limit > 30 {
		d.Limit = 30
	}
	hits := map[string]*searchHit{}
	lexical, err := a.DB.Query(r.Context(), `SELECT source_type,source_id::text,left(body,800),
 (coalesce(ts_rank_cd(search_tsv,plainto_tsquery('simple',$2)),0)+0.15*similarity(body,$2))::float8 AS score
 FROM mimo.search_chunks WHERE owner_id=$1
 AND (search_tsv @@ plainto_tsquery('simple',$2) OR body ILIKE '%' || $2 || '%')
 AND (source_type<>'memory' OR EXISTS (SELECT 1 FROM mimo.memory_entries m WHERE m.id=source_id AND m.scope_type='personal' AND m.scope_id=$1 AND m.status='active' AND (m.expires_at IS NULL OR m.expires_at>now())))
 ORDER BY score DESC LIMIT $3`, a.C.OwnerID, d.Query, d.Limit*3)
	if err != nil {
		internalErr(w, err)
		return
	}
	rank := 0
	for lexical.Next() {
		var h searchHit
		var raw float64
		if err = lexical.Scan(&h.SourceType, &h.SourceID, &h.Snippet, &raw); err != nil {
			break
		}
		rank++
		h.Lexical = true
		h.Score = 1.0 / float64(60+rank)
		hits[h.SourceType+":"+h.SourceID] = &h
	}
	if err == nil {
		err = lexical.Err()
	}
	lexical.Close()
	if err != nil {
		internalErr(w, err)
		return
	}
	semantic := false
	warning := ""
	if a.Embed != nil {
		v, e := a.Embed.Embed(r.Context(), d.Query)
		if e != nil {
			warning = "embedding request failed; lexical results only"
			log.Printf("search embedding provider unavailable: %v", e)
		} else {
			rows, e := a.DB.Query(r.Context(), `SELECT c.source_type,c.source_id::text,left(c.body,800),
 (1-(e.embedding <=> (($3::text)::vector)))::float8 AS similarity
 FROM mimo.search_embeddings e JOIN mimo.search_chunks c ON c.id=e.chunk_id
 WHERE c.owner_id=$1 AND e.model_id=$2 AND vector_dims(e.embedding)=vector_dims(($3::text)::vector)
 AND (c.source_type<>'memory' OR EXISTS (SELECT 1 FROM mimo.memory_entries m WHERE m.id=c.source_id AND m.scope_type='personal' AND m.scope_id=$1 AND m.status='active' AND (m.expires_at IS NULL OR m.expires_at>now())))
 ORDER BY e.embedding <=> (($3::text)::vector) LIMIT $4`, a.C.OwnerID, a.C.EmbeddingModel, vectorText(v), d.Limit*3)
			if e != nil {
				warning = "semantic database query failed; lexical results only"
				log.Printf("search vector query failed: %v", e)
			} else {
				rank = 0
				for rows.Next() {
					var h searchHit
					var raw float64
					if e = rows.Scan(&h.SourceType, &h.SourceID, &h.Snippet, &raw); e != nil {
						break
					}
					rank++
					if raw < 0.2 {
						continue
					}
					key := h.SourceType + ":" + h.SourceID
					if existing, ok := hits[key]; ok {
						existing.Semantic = true
						existing.Score += 1.0 / float64(60+rank)
					} else {
						h.Semantic = true
						h.Score = 1.0 / float64(60+rank)
						hits[key] = &h
					}
				}
				if e == nil {
					e = rows.Err()
				}
				rows.Close()
				if e != nil {
					warning = "semantic database rows failed"
				} else {
					semantic = true
				}
			}
		}
	}
	arr := make([]searchHit, 0, len(hits))
	for _, h := range hits {
		if !math.IsNaN(h.Score) {
			arr = append(arr, *h)
		}
	}
	sort.Slice(arr, func(i, j int) bool { return arr[i].Score > arr[j].Score })
	if len(arr) > d.Limit {
		arr = arr[:d.Limit]
	}
	jsonOK(w, map[string]any{"results": arr, "semantic_enabled": semantic, "warning": warning})
}
