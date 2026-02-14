package tui

import (
	"fmt"
	"io"
	"os"
	"strings"
)

func isTextAttachment(mime string) bool {
	mime = strings.ToLower(strings.TrimSpace(mime))
	if strings.HasPrefix(mime, "text/") {
		return true
	}
	switch mime {
	case "application/json", "application/xml", "application/x-yaml", "application/yaml", "application/javascript", "application/typescript":
		return true
	default:
		return false
	}
}

func readAttachmentText(path string, limit int) (string, bool, error) {
	if limit <= 0 {
		limit = defaultTUIAttachmentMaxBytes
	}
	file, err := os.Open(path)
	if err != nil {
		return "", false, err
	}
	defer file.Close()

	data, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if err != nil {
		return "", false, err
	}
	truncated := len(data) > limit
	if truncated {
		data = data[:limit]
	}
	return string(data), truncated, nil
}

// updateQueueIndicator updates the UI to show queued message count.
func (c *Controller) updateQueueIndicator(sess *SessionState) {
	if c == nil || sess == nil || c.app == nil {
		return
	}
	c.mu.Lock()
	count := len(sess.MessageQueue)
	c.mu.Unlock()
	if count > 0 {
		c.runIfCurrentSession(sess, func() {
			c.app.SetStatus(fmt.Sprintf("Streaming... [%d queued]", count))
		})
	}
}

func (c *Controller) clearMessageQueue(sess *SessionState) int {
	if c == nil || sess == nil {
		return 0
	}
	c.mu.Lock()
	count := len(sess.MessageQueue)
	sess.MessageQueue = nil
	c.mu.Unlock()
	return count
}

// dequeueMessage pops the next queued message from the session, if any.
// Returns the message and true if one was available, or zero value and false.
func (c *Controller) dequeueMessage(sess *SessionState) (QueuedMessage, bool) {
	if c == nil || sess == nil {
		return QueuedMessage{}, false
	}
	c.mu.Lock()
	if len(sess.MessageQueue) == 0 {
		c.mu.Unlock()
		return QueuedMessage{}, false
	}

	// Pop the first queued message
	queued := sess.MessageQueue[0]
	sess.MessageQueue = sess.MessageQueue[1:]
	queued.Acknowledged = true

	// Show acknowledgment in UI
	remaining := len(sess.MessageQueue)
	c.mu.Unlock()

	ackMsg := fmt.Sprintf("Processing queued message from %s", queued.Timestamp.Format("15:04:05"))
	if remaining > 0 {
		ackMsg += fmt.Sprintf(" (%d more queued)", remaining)
	}
	c.runIfCurrentSession(sess, func() {
		if c.app != nil {
			c.app.AddMessage(ackMsg, "system")
		}
	})

	return queued, true
}
