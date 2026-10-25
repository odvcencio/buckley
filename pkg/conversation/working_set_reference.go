package conversation

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"

	"m31labs.dev/buckley/pkg/storage"
)

var ErrWorkingSetChanged = errors.New("stored history no longer matches the released working view")

type workingSetMessageReader interface {
	GetWorkingSetMessages(context.Context, string, []int64, int) ([]storage.Message, error)
}

// WorkingSetReference retains identities and local omission markers, without the
// persisted bodies. It is scoped to one conversation and one exact working view.
type WorkingSetReference struct {
	sessionID string
	positions []int64
	local     map[int]Message
	digest    [32]byte
	readBytes int
}

// ReleaseWorkingSet drops bodies only after verifying that storage can reproduce
// the current view. Unsaved messages and memory-blocked views remain resident.
func (c *Conversation) ReleaseWorkingSet(store workingSetMessageReader) (*WorkingSetReference, error) {
	if len(c.Messages) == 0 || len(c.Messages) > 256 || c.memoryBlocked != nil {
		return nil, nil
	}
	for _, msg := range c.Messages {
		if !msg.workingOnly && msg.storageID <= 0 {
			return nil, nil
		}
	}
	ids, digest, bytes, err := workingSetIdentity(c.SessionID, c.Messages)
	if err != nil || len(ids) == 0 {
		return nil, err
	}
	reference := &WorkingSetReference{sessionID: c.SessionID, digest: digest, readBytes: bytes,
		positions: make([]int64, len(c.Messages)), local: make(map[int]Message)}
	for i, msg := range c.Messages {
		if msg.workingOnly {
			reference.local[i] = msg
		} else {
			reference.positions[i] = msg.storageID
		}
	}
	if _, err := c.readReferencedWorkingSet(store, reference, ids); err != nil {
		return nil, err
	}
	c.Messages = nil
	return reference, nil
}

// RestoreWorkingSet restores the exact released order without resetting token,
// compaction or prefix bookkeeping. Failure leaves the reference retryable.
func (c *Conversation) RestoreWorkingSet(store workingSetMessageReader, reference *WorkingSetReference) error {
	if reference == nil || reference.sessionID != c.SessionID || len(c.Messages) != 0 {
		return fmt.Errorf("invalid working-set reference for session %s", c.SessionID)
	}
	ids := make([]int64, 0, len(reference.positions))
	for _, id := range reference.positions {
		if id > 0 {
			ids = append(ids, id)
		}
	}
	persisted, err := c.readReferencedWorkingSet(store, reference, ids)
	if err != nil {
		return err
	}
	messages := make([]Message, len(reference.positions))
	index := 0
	for i, id := range reference.positions {
		if id == 0 {
			messages[i] = reference.local[i]
		} else {
			messages[i] = persisted[index]
			index++
		}
	}
	c.Messages = messages
	return nil
}

func (c *Conversation) readReferencedWorkingSet(store workingSetMessageReader, reference *WorkingSetReference, ids []int64) ([]Message, error) {
	rows, err := store.GetWorkingSetMessages(context.Background(), c.SessionID, ids, reference.readBytes)
	if err != nil {
		return nil, fmt.Errorf("read referenced history: %w", err)
	}
	for _, row := range rows {
		if row.SessionID != c.SessionID {
			return nil, ErrWorkingSetChanged
		}
	}
	restored := New(c.SessionID)
	restored.loadStorageMessages(rows)
	_, digest, _, err := workingSetIdentity(c.SessionID, restored.Messages)
	if err != nil {
		return nil, err
	}
	if digest != reference.digest {
		return nil, ErrWorkingSetChanged
	}
	return restored.Messages, nil
}

func workingSetIdentity(sessionID string, messages []Message) ([]int64, [32]byte, int, error) {
	ids := make([]int64, 0, len(messages))
	hash := sha256.New()
	encoder := json.NewEncoder(hash)
	bytes := 0
	for _, msg := range messages {
		if msg.workingOnly {
			continue
		}
		row, err := StorageMessage(sessionID, msg)
		if err != nil {
			return nil, [32]byte{}, 0, err
		}
		row.ID = msg.storageID
		row.Timestamp = row.Timestamp.UTC()
		if err := encoder.Encode(row); err != nil {
			return nil, [32]byte{}, 0, err
		}
		ids = append(ids, msg.storageID)
		bytes += row.WorkingSetReadBytes()
	}
	var digest [32]byte
	copy(digest[:], hash.Sum(nil))
	return ids, digest, bytes, nil
}
