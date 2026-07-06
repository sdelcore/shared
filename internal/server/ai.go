package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

type aiChatRequest struct {
	Messages []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"messages"`
	System    string `json:"system"`
	Model     string `json:"model"`
	MaxTokens int64  `json:"max_tokens"`
	Stream    bool   `json:"stream"`
}

type oaMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type aiImageRequest struct {
	Prompt string `json:"prompt"`
	Model  string `json:"model"`
	Size   string `json:"size"`
}

// handleAIChat proxies to an OpenAI-compatible chat-completions endpoint
// (OPENAI_BASE_URL, e.g. an LiteLLM gateway or api.openai.com). The key stays
// on the server. Request/response shape is unchanged for site clients.
func (s *Server) handleAIChat(w http.ResponseWriter, r *http.Request) {
	baseURL := strings.TrimRight(os.Getenv("OPENAI_BASE_URL"), "/")
	apiKey := os.Getenv("OPENAI_API_KEY")
	if baseURL == "" || apiKey == "" {
		writeErr(w, http.StatusServiceUnavailable, "ai not configured: set OPENAI_BASE_URL and OPENAI_API_KEY")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req aiChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	if len(req.Messages) == 0 {
		writeErr(w, http.StatusBadRequest, "messages must not be empty")
		return
	}

	msgs := make([]oaMsg, 0, len(req.Messages)+1)
	if req.System != "" {
		msgs = append(msgs, oaMsg{Role: "system", Content: req.System})
	}
	for _, m := range req.Messages {
		if m.Role != "user" && m.Role != "assistant" {
			writeErr(w, http.StatusBadRequest, "invalid role: "+m.Role)
			return
		}
		msgs = append(msgs, oaMsg{Role: m.Role, Content: m.Content})
	}

	model := req.Model
	if model == "" {
		model = os.Getenv("SHARED_AI_MODEL")
	}
	if model == "" {
		model = "claude-opus-4-8"
	}
	maxTokens := req.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 16000
	}

	if req.Stream {
		s.streamAIChat(w, r, baseURL, apiKey, model, maxTokens, msgs)
		return
	}

	body, _ := json.Marshal(map[string]any{
		"model":      model,
		"max_tokens": maxTokens,
		"messages":   msgs,
	})

	// Non-streaming, so the deadline must cover the whole generation. Scale
	// with max_tokens at a conservative 25 tok/s (plus 60s of overhead),
	// clamped to a sane [120s, 600s] window.
	timeout := time.Duration(60+maxTokens/25) * time.Second
	if timeout < 120*time.Second {
		timeout = 120 * time.Second
	}
	if timeout > 600*time.Second {
		timeout = 600 * time.Second
	}
	ctx, cancel := context.WithTimeout(r.Context(), timeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)

	httpResp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	defer httpResp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(httpResp.Body, 1<<20))
	if httpResp.StatusCode != http.StatusOK {
		writeErr(w, http.StatusBadGateway, "upstream "+httpResp.Status+": "+string(respBody))
		return
	}

	var oaResp struct {
		Model   string `json:"model"`
		Choices []struct {
			FinishReason string `json:"finish_reason"`
			Message      struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(respBody, &oaResp); err != nil {
		writeErr(w, http.StatusBadGateway, "invalid upstream response: "+err.Error())
		return
	}

	content, stop := "", ""
	if len(oaResp.Choices) > 0 {
		content = oaResp.Choices[0].Message.Content
		stop = oaResp.Choices[0].FinishReason
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"content":     content,
		"model":       oaResp.Model,
		"stop_reason": stop,
	})
}

// streamAIChat proxies a streaming chat completion, piping the upstream
// OpenAI-compatible SSE straight through to the client. Because data flows
// incrementally there is no per-token deadline; a generous overall cap guards
// against a stuck upstream.
func (s *Server) streamAIChat(w http.ResponseWriter, r *http.Request, baseURL, apiKey, model string, maxTokens int64, msgs []oaMsg) {
	body, _ := json.Marshal(map[string]any{
		"model":      model,
		"max_tokens": maxTokens,
		"messages":   msgs,
		"stream":     true,
	})

	ctx, cancel := context.WithTimeout(r.Context(), 600*time.Second)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)
	httpReq.Header.Set("Accept", "text/event-stream")

	httpResp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	defer httpResp.Body.Close()
	if httpResp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(io.LimitReader(httpResp.Body, 1<<20))
		writeErr(w, http.StatusBadGateway, "upstream "+httpResp.Status+": "+string(respBody))
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)

	buf := make([]byte, 4096)
	for {
		n, rerr := httpResp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		}
		if rerr != nil {
			return
		}
	}
}

// handleAIImage proxies to an OpenAI-compatible image-generation endpoint,
// decodes the returned base64 image, and stores it in the site's uploads dir
// so the site gets a persistent /uploads URL. Scoped per site by Host.
func (s *Server) handleAIImage(w http.ResponseWriter, r *http.Request) {
	baseURL := strings.TrimRight(os.Getenv("OPENAI_BASE_URL"), "/")
	apiKey := os.Getenv("OPENAI_API_KEY")
	if baseURL == "" || apiKey == "" {
		writeErr(w, http.StatusServiceUnavailable, "ai not configured: set OPENAI_BASE_URL and OPENAI_API_KEY")
		return
	}

	site := s.siteFromRequest(r)
	if site == "" || !validSite(site) {
		writeErr(w, http.StatusBadRequest, "invalid or missing site")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var req aiImageRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
		return
	}
	if req.Prompt == "" {
		writeErr(w, http.StatusBadRequest, "prompt must not be empty")
		return
	}

	model := req.Model
	if model == "" {
		model = os.Getenv("SHARED_AI_IMAGE_MODEL")
	}
	if model == "" {
		writeErr(w, http.StatusServiceUnavailable, "ai image model not configured: set SHARED_AI_IMAGE_MODEL or pass model")
		return
	}

	payload := map[string]any{
		"model":           model,
		"prompt":          req.Prompt,
		"response_format": "b64_json",
	}
	if req.Size != "" {
		payload["size"] = req.Size
	}
	body, _ := json.Marshal(payload)

	ctx, cancel := context.WithTimeout(r.Context(), 300*time.Second)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/images/generations", bytes.NewReader(body))
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+apiKey)

	httpResp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	defer httpResp.Body.Close()
	respBody, _ := io.ReadAll(io.LimitReader(httpResp.Body, maxUploadSize))
	if httpResp.StatusCode != http.StatusOK {
		writeErr(w, http.StatusBadGateway, "upstream "+httpResp.Status+": "+string(respBody))
		return
	}

	var oaResp struct {
		Data []struct {
			B64JSON string `json:"b64_json"`
		} `json:"data"`
	}
	if err := json.Unmarshal(respBody, &oaResp); err != nil {
		writeErr(w, http.StatusBadGateway, "invalid upstream response: "+err.Error())
		return
	}
	if len(oaResp.Data) == 0 || oaResp.Data[0].B64JSON == "" {
		writeErr(w, http.StatusBadGateway, "no image in upstream response")
		return
	}
	raw, err := base64.StdEncoding.DecodeString(oaResp.Data[0].B64JSON)
	if err != nil {
		writeErr(w, http.StatusBadGateway, "invalid image data: "+err.Error())
		return
	}

	url, err := s.saveUpload(site, "ai.png", bytes.NewReader(raw))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "could not save image")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"url": url})
}
