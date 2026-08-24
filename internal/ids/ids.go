package ids

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
)

type Generator interface {
	New(prefix string) (string, error)
}

type Crypto struct{}

func (Crypto) New(prefix string) (string, error) {
	if prefix == "" || strings.ContainsAny(prefix, " /\\") {
		return "", fmt.Errorf("invalid id prefix %q", prefix)
	}
	buffer := make([]byte, 12)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("read secure random id: %w", err)
	}
	return prefix + "_" + hex.EncodeToString(buffer), nil
}

type Sequence struct {
	Next int
}

func (s *Sequence) New(prefix string) (string, error) {
	if prefix == "" {
		return "", fmt.Errorf("prefix is required")
	}
	s.Next++
	return fmt.Sprintf("%s_%06d", prefix, s.Next), nil
}
