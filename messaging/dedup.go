package messaging

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const durableSeenMessageTTL = 10 * time.Minute

var durableSeenMessagesRootOverride string

func durableSeenMessagesRoot() (string, error) {
	if durableSeenMessagesRootOverride != "" {
		return durableSeenMessagesRootOverride, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}
	return filepath.Join(home, ".weclaw", "runtime", "seen_messages"), nil
}

func claimDurableSeenMessage(userID string, messageID int64, now time.Time) (bool, error) {
	root, err := durableSeenMessagesRoot()
	if err != nil {
		return false, err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return false, fmt.Errorf("create durable seen message dir: %w", err)
	}

	path := filepath.Join(root, durableSeenMessageFilename(userID, messageID))
	if ok, err := createSeenMessageMarker(path, now); ok || err != nil {
		return ok, err
	}

	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return createSeenMessageMarker(path, now)
		}
		return false, fmt.Errorf("stat durable seen message marker: %w", err)
	}
	if now.Sub(info.ModTime()) <= durableSeenMessageTTL {
		return false, nil
	}

	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return false, fmt.Errorf("remove expired durable seen message marker: %w", err)
	}
	return createSeenMessageMarker(path, now)
}

func createSeenMessageMarker(path string, now time.Time) (bool, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("create durable seen message marker: %w", err)
	}
	defer file.Close()

	if _, err := file.WriteString(now.UTC().Format(time.RFC3339Nano) + "\n"); err != nil {
		return false, fmt.Errorf("write durable seen message marker: %w", err)
	}
	return true, nil
}

func cleanupDurableSeenMessages(now time.Time) {
	root, err := durableSeenMessagesRoot()
	if err != nil {
		return
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if now.Sub(info.ModTime()) <= durableSeenMessageTTL {
			continue
		}
		_ = os.Remove(filepath.Join(root, entry.Name()))
	}
}

func durableSeenMessageFilename(userID string, messageID int64) string {
	return fmt.Sprintf("%s-%d.seen", sanitizeDedupSegment(userID), messageID)
}

func sanitizeDedupSegment(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "user"
	}
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '.', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	if b.Len() == 0 {
		return "user"
	}
	return b.String()
}
