package server

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
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

func (s *Server) handleAIChat(w http.ResponseWriter, r *http.Request) {
	if os.Getenv("ANTHROPIC_API_KEY") == "" {
		writeErr(w, http.StatusServiceUnavailable, "ai not configured: set ANTHROPIC_API_KEY")
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

	msgs := make([]anthropic.MessageParam, 0, len(req.Messages))
	for _, m := range req.Messages {
		switch m.Role {
		case "user":
			msgs = append(msgs, anthropic.NewUserMessage(anthropic.NewTextBlock(m.Content)))
		case "assistant":
			msgs = append(msgs, anthropic.NewAssistantMessage(anthropic.NewTextBlock(m.Content)))
		default:
			writeErr(w, http.StatusBadRequest, "invalid role: "+m.Role)
			return
		}
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

	params := anthropic.MessageNewParams{
		Model:     anthropic.Model(model),
		MaxTokens: maxTokens,
		Messages:  msgs,
	}
	if req.System != "" {
		params.System = []anthropic.TextBlockParam{{Text: req.System}}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 120*time.Second)
	defer cancel()

	client := anthropic.NewClient()
	resp, err := client.Messages.New(ctx, params)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}

	var sb strings.Builder
	for _, block := range resp.Content {
		switch v := block.AsAny().(type) {
		case anthropic.TextBlock:
			sb.WriteString(v.Text)
		}
	}

	writeJSON(w, http.StatusOK, map[string]string{
		"content":     sb.String(),
		"model":       string(resp.Model),
		"stop_reason": string(resp.StopReason),
	})
}
