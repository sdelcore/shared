package server

import (
	"bytes"
	"context"
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

	type oaMsg struct {
		Role    string `json:"role"`
		Content string `json:"content"`
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

	body, _ := json.Marshal(map[string]any{
		"model":      model,
		"max_tokens": maxTokens,
		"messages":   msgs,
	})

	ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
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
