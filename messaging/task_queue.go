package messaging

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fastclaw-ai/weclaw/ilink"
	"github.com/google/uuid"
)

const (
	defaultTaskPrefix          = "任务:"
	defaultTaskPrefixFullWidth = "任务："
	taskQueueVersion           = "wechat-task-queue-v1"
)

type TaskQueueState struct {
	Version   string       `json:"version"`
	UpdatedAt string       `json:"updated_at"`
	Tasks     []QueuedTask `json:"tasks"`
}

type QueuedTask struct {
	ID              string      `json:"id"`
	Status          string      `json:"status"`
	TaskText        string      `json:"task_text"`
	RawText         string      `json:"raw_text"`
	EnqueuedAt      string      `json:"enqueued_at"`
	StartedAt       string      `json:"started_at,omitempty"`
	CompletedAt     string      `json:"completed_at,omitempty"`
	Attempts        int         `json:"attempts"`
	DispatchedMode  string      `json:"dispatched_mode,omitempty"`
	DispatchedAgent string      `json:"dispatched_agent,omitempty"`
	Source          TaskSource  `json:"source"`
	LastResult      *TaskResult `json:"last_result,omitempty"`
	LastError       string      `json:"last_error,omitempty"`
}

type TaskSource struct {
	Trigger      string `json:"trigger"`
	FromUserID   string `json:"from_user_id"`
	MessageID    int64  `json:"message_id"`
	ContextToken string `json:"context_token,omitempty"`
	ReceivedAt   string `json:"received_at"`
}

type TaskResult struct {
	Summary string `json:"summary"`
}

var taskQueueMu sync.Mutex

func ExtractQueuedTask(text string) (string, bool) {
	trimmed := strings.TrimSpace(text)
	for _, prefix := range []string{defaultTaskPrefix, defaultTaskPrefixFullWidth} {
		if strings.HasPrefix(trimmed, prefix) {
			task := strings.TrimSpace(strings.TrimPrefix(trimmed, prefix))
			if task == "" {
				return "", false
			}
			return task, true
		}
	}
	return "", false
}

func taskQueuePath() (string, error) {
	if override := strings.TrimSpace(os.Getenv("LONGCLAW_WECHAT_TASK_QUEUE_FILE")); override != "" {
		return override, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".longclaw", "runtime-v2", "state", "wechat-task-queue.json"), nil
}

func loadTaskQueueState(path string) (TaskQueueState, error) {
	state := TaskQueueState{Version: taskQueueVersion, Tasks: []QueuedTask{}}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return state, nil
		}
		return state, err
	}
	if len(data) == 0 {
		return state, nil
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return state, err
	}
	if state.Version == "" {
		state.Version = taskQueueVersion
	}
	if state.Tasks == nil {
		state.Tasks = []QueuedTask{}
	}
	return state, nil
}

func saveTaskQueueState(path string, state TaskQueueState) error {
	state.Version = taskQueueVersion
	state.UpdatedAt = time.Now().Format(time.RFC3339)
	if state.Tasks == nil {
		state.Tasks = []QueuedTask{}
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	tmpPath := path + ".tmp"
	if err := os.WriteFile(tmpPath, append(data, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func EnqueueQueuedTask(msg ilink.WeixinMessage, rawText, taskText string) (*QueuedTask, error) {
	path, err := taskQueuePath()
	if err != nil {
		return nil, err
	}

	taskQueueMu.Lock()
	defer taskQueueMu.Unlock()

	state, err := loadTaskQueueState(path)
	if err != nil {
		return nil, fmt.Errorf("load task queue: %w", err)
	}

	now := time.Now().Format(time.RFC3339)
	task := QueuedTask{
		ID:         "wx-task-" + uuid.NewString(),
		Status:     "pending",
		TaskText:   strings.TrimSpace(taskText),
		RawText:    strings.TrimSpace(rawText),
		EnqueuedAt: now,
		Attempts:   0,
		Source: TaskSource{
			Trigger:      "wechat_prefix",
			FromUserID:   msg.FromUserID,
			MessageID:    msg.MessageID,
			ContextToken: msg.ContextToken,
			ReceivedAt:   now,
		},
	}

	state.Tasks = append(state.Tasks, task)
	if err := saveTaskQueueState(path, state); err != nil {
		return nil, fmt.Errorf("save task queue: %w", err)
	}
	return &task, nil
}
