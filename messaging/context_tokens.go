package messaging

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type contextTokenRecord struct {
	ContextToken string `json:"context_token"`
	UpdatedAt    string `json:"updated_at"`
}

type contextTokenDB struct {
	Tokens map[string]contextTokenRecord `json:"tokens"`
}

func contextTokensPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".weclaw", "runtime", "context_tokens.json"), nil
}

func loadContextTokens() (contextTokenDB, error) {
	path, err := contextTokensPath()
	if err != nil {
		return contextTokenDB{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return contextTokenDB{Tokens: map[string]contextTokenRecord{}}, nil
		}
		return contextTokenDB{}, fmt.Errorf("read context token db: %w", err)
	}
	var db contextTokenDB
	if err := json.Unmarshal(data, &db); err != nil {
		return contextTokenDB{}, fmt.Errorf("unmarshal context token db: %w", err)
	}
	if db.Tokens == nil {
		db.Tokens = map[string]contextTokenRecord{}
	}
	return db, nil
}

func saveContextTokens(db contextTokenDB) error {
	path, err := contextTokensPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create runtime dir: %w", err)
	}
	data, err := json.MarshalIndent(db, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal context token db: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("write context token db: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("rename context token db: %w", err)
	}
	return nil
}

func RememberContextToken(userID, contextToken string) error {
	if userID == "" || contextToken == "" {
		return nil
	}
	db, err := loadContextTokens()
	if err != nil {
		return err
	}
	db.Tokens[userID] = contextTokenRecord{
		ContextToken: contextToken,
		UpdatedAt:    time.Now().UTC().Format(time.RFC3339),
	}
	return saveContextTokens(db)
}

func LookupContextToken(userID string) (string, bool) {
	if userID == "" {
		return "", false
	}
	db, err := loadContextTokens()
	if err != nil {
		return "", false
	}
	record, ok := db.Tokens[userID]
	if !ok || record.ContextToken == "" {
		return "", false
	}
	return record.ContextToken, true
}
