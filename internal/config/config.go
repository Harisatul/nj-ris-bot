package config

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	DatabaseURL, APIToken, OwnerID, ListenAddr      string
	R2AccountID, R2AccessKey, R2SecretKey, R2Bucket string
	EmbeddingURL, EmbeddingKey, EmbeddingModel      string
	EmbeddingDimensions                             int
	MaxUploadBytes                                  int64
}

func Env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
func Load() (Config, error) {
	c := Config{
		DatabaseURL: os.Getenv("DATABASE_URL"), APIToken: os.Getenv("MIMO_API_TOKEN"),
		OwnerID: Env("MIMO_OWNER_ID", "me"), ListenAddr: Env("LISTEN_ADDR", "127.0.0.1:8080"),
		R2AccountID: os.Getenv("R2_ACCOUNT_ID"), R2AccessKey: os.Getenv("R2_ACCESS_KEY_ID"),
		R2SecretKey: os.Getenv("R2_SECRET_ACCESS_KEY"), R2Bucket: os.Getenv("R2_BUCKET"),
		EmbeddingURL: Env("EMBEDDING_URL", os.Getenv("EMBEDDING_API_URL")), EmbeddingKey: os.Getenv("EMBEDDING_API_KEY"),
		EmbeddingModel: Env("EMBEDDING_MODEL", "embeddinggemma-2:440m"), EmbeddingDimensions: 768, MaxUploadBytes: 15 << 20,
	}
	if v := os.Getenv("EMBEDDING_DIMENSIONS"); v != "" {
		n, e := strconv.Atoi(v)
		if e != nil || n != 768 {
			return c, errors.New("EMBEDDING_DIMENSIONS must be 768 for semantic memory")
		}
		c.EmbeddingDimensions = n
	}
	if v := os.Getenv("MAX_UPLOAD_MB"); v != "" {
		n, e := strconv.ParseInt(v, 10, 64)
		if e != nil || n < 1 || n > 100 {
			return c, errors.New("MAX_UPLOAD_MB must be 1..100")
		}
		c.MaxUploadBytes = n << 20
	}
	if c.DatabaseURL == "" {
		return c, errors.New("DATABASE_URL required")
	}
	if len(c.APIToken) < 24 {
		return c, errors.New("MIMO_API_TOKEN must contain at least 24 characters")
	}
	if strings.TrimSpace(c.OwnerID) == "" {
		return c, errors.New("MIMO_OWNER_ID required")
	}
	return c, nil
}
func (c Config) HasR2() bool {
	return c.R2AccountID != "" && c.R2AccessKey != "" && c.R2SecretKey != "" && c.R2Bucket != ""
}
func (c Config) HasEmbedding() bool { return c.EmbeddingURL != "" && c.EmbeddingModel != "" }
func WIB() *time.Location           { return time.FixedZone("WIB", 7*60*60) }
