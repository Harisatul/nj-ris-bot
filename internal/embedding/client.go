package embedding

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Client struct {
	URL, Key, Model string
	Dimensions      int
	HTTP            *http.Client
}

func New(url, key, model string, dimensions int) *Client {
	if dimensions <= 0 {
		dimensions = 768
	}
	return &Client{URL: url, Key: key, Model: model, Dimensions: dimensions, HTTP: &http.Client{Timeout: 30 * time.Second}}
}
func (c *Client) Embed(ctx context.Context, input string) ([]float64, error) {
	vectors, err := c.EmbedBatch(ctx, []string{input})
	if err != nil {
		return nil, err
	}
	return vectors[0], nil
}
func (c *Client) EmbedBatch(ctx context.Context, inputs []string) ([][]float64, error) {
	if c.URL == "" || c.Model == "" {
		return nil, errors.New("embedding provider not configured")
	}
	if len(inputs) == 0 || len(inputs) > 64 {
		return nil, errors.New("embedding batch must contain 1..64 inputs")
	}
	payload, _ := json.Marshal(map[string]any{"model": c.Model, "input": inputs})
	var last error
	for attempt := 0; attempt < 3; attempt++ {
		if attempt > 0 {
			delay := time.Duration(1<<(attempt-1)) * 200 * time.Millisecond
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil, ctx.Err()
			case <-timer.C:
			}
		}
		result, retry, err := c.doRequest(ctx, payload, len(inputs))
		if err == nil {
			return result, nil
		}
		last = err
		if !retry {
			break
		}
	}
	return nil, last
}
func (c *Client) doRequest(ctx context.Context, payload []byte, expected int) ([][]float64, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(payload))
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.Key != "" {
		req.Header.Set("Authorization", "Bearer "+c.Key)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, true, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return nil, resp.StatusCode == 408 || resp.StatusCode == 429 || resp.StatusCode >= 500, fmt.Errorf("embedding request status %d: %s", resp.StatusCode, string(b))
	}
	var out struct {
		Embeddings [][]float64 `json:"embeddings"`
		Data       []struct {
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 20<<20)).Decode(&out); err != nil {
		return nil, false, err
	}
	vectors := out.Embeddings
	if len(vectors) == 0 {
		for _, item := range out.Data {
			vectors = append(vectors, item.Embedding)
		}
	}
	if len(vectors) != expected {
		return nil, false, fmt.Errorf("embedding response count %d, expected %d", len(vectors), expected)
	}
	for _, vector := range vectors {
		if len(vector) != c.Dimensions {
			return nil, false, fmt.Errorf("embedding dimension %d, expected %d", len(vector), c.Dimensions)
		}
	}
	return vectors, false, nil
}
func ContentHash(input string) string {
	sum := sha256.Sum256([]byte(input))
	return fmt.Sprintf("%x", sum[:])
}
func VectorText(v []float64) string {
	parts := make([]string, len(v))
	for i, n := range v {
		parts[i] = fmt.Sprintf("%.9g", n)
	}
	return "[" + strings.Join(parts, ",") + "]"
}
