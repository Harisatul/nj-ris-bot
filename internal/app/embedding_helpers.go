package app

import "github.com/mimo/personal-os/internal/embedding"

func vectorText(v []float64) string { return embedding.VectorText(v) }
