package embedding

import (
	"bytes"
	"context"
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
	HTTP            *http.Client
}

func New(url, key, model string) *Client {
	return &Client{URL: url, Key: key, Model: model, HTTP: &http.Client{Timeout: 25 * time.Second}}
}
func (c *Client) Embed(ctx context.Context, input string) ([]float64, error) {
	if c.URL == "" || c.Model == "" {
		return nil, errors.New("embedding provider not configured")
	}
	payload, _ := json.Marshal(map[string]any{"model": c.Model, "input": input})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.URL, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.Key != "" {
		req.Header.Set("Authorization", "Bearer "+c.Key)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
		return nil, fmt.Errorf("embedding request status %d: %s", resp.StatusCode, string(b))
	}
	var out struct {
		Data []struct {
			Embedding []float64 `json:"embedding"`
		} `json:"data"`
	}
	if err = json.NewDecoder(io.LimitReader(resp.Body, 5<<20)).Decode(&out); err != nil {
		return nil, err
	}
	if len(out.Data) == 0 || len(out.Data[0].Embedding) == 0 || len(out.Data[0].Embedding) > 2000 {
		return nil, errors.New("embedding empty or vector dimension outside 1..2000")
	}
	return out.Data[0].Embedding, nil
}
func VectorText(v []float64) string {
	parts := make([]string, len(v))
	for i, n := range v {
		parts[i] = fmt.Sprintf("%.9g", n)
	}
	return "[" + strings.Join(parts, ",") + "]"
}
