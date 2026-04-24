package app

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

func NewRunID() string {
	return newID("r")
}

func NewAttemptID() string {
	return newID("a")
}

func newID(prefix string) string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	}
	return fmt.Sprintf("%s_%s", prefix, hex.EncodeToString(b[:]))
}
