package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"

	"github.com/fastclaw-ai/weclaw/ilink"
	"github.com/fastclaw-ai/weclaw/messaging"
)

// Server provides an HTTP API for sending messages.
type Server struct {
	clients []*ilink.Client
	addr    string
	handler *messaging.Handler
}

// NewServer creates an API server.
func NewServer(clients []*ilink.Client, addr string, handler *messaging.Handler) *Server {
	if addr == "" {
		addr = "127.0.0.1:18011"
	}
	return &Server{clients: clients, addr: addr, handler: handler}
}

// SendRequest is the JSON body for POST /api/send.
type SendRequest struct {
	To       string `json:"to"`
	Text     string `json:"text,omitempty"`
	MediaURL string `json:"media_url,omitempty"` // image/video/file URL
}

type TaskDispatchRequest struct {
	To     string `json:"to"`
	Text   string `json:"text"`
	TaskID string `json:"task_id,omitempty"`
}

type AgentOSLaunchRequest struct {
	Text         string   `json:"text"`
	FromUserID   string   `json:"from_user_id,omitempty"`
	ToUserID     string   `json:"to_user_id,omitempty"`
	ContextToken string   `json:"context_token,omitempty"`
	MessageID    int64    `json:"message_id,omitempty"`
	TargetAgents []string `json:"target_agents,omitempty"`
}

// Run starts the HTTP server. Blocks until ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/send", s.handleSend)
	mux.HandleFunc("/api/task-dispatch", s.handleTaskDispatch)
	mux.HandleFunc("/api/agent-os/launch", s.handleAgentOSLaunch)
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "ok")
	})

	srv := &http.Server{Addr: s.addr, Handler: mux}

	go func() {
		<-ctx.Done()
		srv.Shutdown(context.Background())
	}()

	log.Printf("[api] listening on %s", s.addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return err
	}
	return nil
}

func (s *Server) handleSend(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}

	var req SendRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}

	if req.To == "" {
		http.Error(w, `"to" is required`, http.StatusBadRequest)
		return
	}
	if req.Text == "" && req.MediaURL == "" {
		http.Error(w, `"text" or "media_url" is required`, http.StatusBadRequest)
		return
	}

	if len(s.clients) == 0 {
		http.Error(w, "no accounts configured", http.StatusServiceUnavailable)
		return
	}

	// Use the first client
	client := s.clients[0]
	ctx := r.Context()
	contextToken, hasContextToken := messaging.LookupContextToken(req.To)

	// Send text if provided
	if req.Text != "" {
		if err := messaging.SendTextReply(ctx, client, req.To, req.Text, contextToken, ""); err != nil {
			log.Printf("[api] send text failed: %v", err)
			if !hasContextToken {
				http.Error(w, "send text failed: "+err.Error()+" (no persisted context token; send the bot a fresh message first)", http.StatusInternalServerError)
				return
			}
			http.Error(w, "send text failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		log.Printf("[api] sent text to %s: %q", req.To, req.Text)

		// Extract and send any markdown images embedded in text
		for _, imgURL := range messaging.ExtractImageURLs(req.Text) {
			if err := messaging.SendMediaFromURL(ctx, client, req.To, imgURL, ""); err != nil {
				log.Printf("[api] send extracted image failed: %v", err)
			} else {
				log.Printf("[api] sent extracted image to %s: %s", req.To, imgURL)
			}
		}
	}

	// Send media if provided
	if req.MediaURL != "" {
		if err := messaging.SendMediaFromURL(ctx, client, req.To, req.MediaURL, contextToken); err != nil {
			log.Printf("[api] send media failed: %v", err)
			if !hasContextToken {
				http.Error(w, "send media failed: "+err.Error()+" (no persisted context token; send the bot a fresh message first)", http.StatusInternalServerError)
				return
			}
			http.Error(w, "send media failed: "+err.Error(), http.StatusInternalServerError)
			return
		}
		log.Printf("[api] sent media to %s: %s", req.To, req.MediaURL)
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}

func (s *Server) handleTaskDispatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if s.handler == nil {
		http.Error(w, "handler unavailable", http.StatusServiceUnavailable)
		return
	}
	if len(s.clients) == 0 {
		http.Error(w, "no accounts configured", http.StatusServiceUnavailable)
		return
	}

	var req TaskDispatchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.To == "" {
		http.Error(w, `"to" is required`, http.StatusBadRequest)
		return
	}
	if req.Text == "" {
		http.Error(w, `"text" is required`, http.StatusBadRequest)
		return
	}

	result, err := s.handler.DispatchQueuedTask(r.Context(), s.clients[0], req.To, req.Text)
	if err != nil {
		log.Printf("[api] task dispatch failed task_id=%s to=%s: %v", req.TaskID, req.To, err)
		http.Error(w, "task dispatch failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	log.Printf("[api] task dispatched task_id=%s to=%s mode=%s agent=%s", req.TaskID, req.To, result.Mode, result.Agent)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"status":        "ok",
		"mode":          result.Mode,
		"agent":         result.Agent,
		"reply_preview": result.ReplyPreview,
	})
}

func (s *Server) handleAgentOSLaunch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if s.handler == nil {
		http.Error(w, "handler unavailable", http.StatusServiceUnavailable)
		return
	}

	var req AgentOSLaunchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if req.Text == "" {
		http.Error(w, `"text" is required`, http.StatusBadRequest)
		return
	}

	msg := ilink.WeixinMessage{
		FromUserID:   req.FromUserID,
		ToUserID:     req.ToUserID,
		ContextToken: req.ContextToken,
		MessageID:    req.MessageID,
	}
	if msg.FromUserID == "" {
		msg.FromUserID = "simulation@im.wechat"
	}
	if msg.MessageID == 0 {
		msg.MessageID = 1
	}

	if err := s.handler.SubmitAgentOSFrontDoor(r.Context(), msg, req.Text, req.TargetAgents); err != nil {
		log.Printf("[api] agent-os launch failed from=%s: %v", msg.FromUserID, err)
		http.Error(w, "agent-os launch failed: "+err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"status":        "ok",
		"from_user_id":  msg.FromUserID,
		"message_id":    msg.MessageID,
		"target_agents": req.TargetAgents,
	})
}
