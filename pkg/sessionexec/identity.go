package sessionexec

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/oklog/ulid/v2"
)

const digestSchema = "buckley.sessionexec.v1"

// NewCommandID returns a sortable opaque command identifier. It is safe to
// call before opening a transaction so retries retain the same identity.
func NewCommandID() string {
	return strings.ToLower(ulid.Make().String())
}

// RunIDForSession returns the one stable foreground run identity for a
// session. It deliberately does not contain the session identifier.
func RunIDForSession(sessionID string) string {
	digest := hashParts("foreground-run", sessionID)
	return "run_" + digest
}

// TurnID returns a stable, opaque turn identity for a command generation.
func TurnID(commandID string, generation int) string {
	digest := hashParts("foreground-turn", commandID, fmt.Sprintf("%d", generation))
	return "turn_" + digest
}

// LaneFor maps the bounded command vocabulary to its scheduling lane.
func LaneFor(commandType string) (Lane, error) {
	switch strings.TrimSpace(strings.ToLower(commandType)) {
	case "input", "queue", "steer", "model", "slash":
		return LaneWork, nil
	case "interrupt", "approval", "pause", "resume":
		return LaneControl, nil
	default:
		return "", validationError("unsupported command type")
	}
}

// inputEnvelopeDigest binds every acceptance field that may affect execution. The
// length-prefixed encoding prevents concatenation ambiguity without relying
// on a map or implementation-specific JSON ordering.
func inputEnvelopeDigest(req AcceptRequest, commandID string, lane Lane, targetCommandID string) (string, error) {
	if err := ValidateAcceptRequest(req, commandID); err != nil {
		return "", err
	}
	if expected, err := LaneFor(req.Type); err != nil || expected != lane {
		return "", validationError("command lane mismatch")
	}
	if targetCommandID != "" {
		if err := validateIdentifier("target command id", targetCommandID, MaxCommandIDBytes); err != nil {
			return "", err
		}
	}
	return hashParts(
		"acceptance",
		req.SessionID,
		commandID,
		strings.ToLower(req.Type),
		req.Content,
		req.AcceptedBy,
		string(lane),
		targetCommandID,
	), nil
}

// InputDigest binds the immutable acceptance envelope and execution identity.
// This lets an adapter detect valid-looking changes to sequence, generation,
// run, task, or turn fields before claiming work.
func InputDigest(req AcceptRequest, identity Identity, lane Lane, targetCommandID string) (string, error) {
	if identity.SessionID != req.SessionID || identity.CommandID == "" || identity.CommandID != req.CommandID {
		return "", validationError("acceptance identity mismatch")
	}
	if identity.RunID != RunIDForSession(identity.SessionID) || identity.TaskID != ForegroundTaskID ||
		identity.TurnID != TurnID(identity.CommandID, identity.Generation) || identity.Generation != 0 ||
		identity.Sequence < 1 || identity.Sequence > MaxCommandSequence {
		return "", validationError("acceptance identity is invalid")
	}
	input, err := inputEnvelopeDigest(req, identity.CommandID, lane, targetCommandID)
	if err != nil {
		return "", err
	}
	return hashParts(
		"accepted-command",
		input,
		identity.RunID,
		identity.TaskID,
		identity.TurnID,
		fmt.Sprintf("%d", identity.Generation),
		fmt.Sprintf("%d", identity.Sequence),
	), nil
}

func hashParts(kind string, parts ...string) string {
	h := sha256.New()
	writeDigestPart(h, digestSchema)
	writeDigestPart(h, kind)
	for _, part := range parts {
		writeDigestPart(h, part)
	}
	return hex.EncodeToString(h.Sum(nil))
}

type digestWriter interface {
	Write([]byte) (int, error)
}

func writeDigestPart(w digestWriter, value string) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	_, _ = w.Write(size[:])
	_, _ = w.Write([]byte(value))
}
