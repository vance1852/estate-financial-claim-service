package domain

import (
	"crypto/sha256"
	"encoding/hex"
	"regexp"
	"strings"
	"unicode/utf8"
)

type Role string

const (
	RoleClaimant   Role = "claimant"
	RoleOfficer    Role = "case_officer"
	RoleSupervisor Role = "supervisor"
)

func (r Role) Valid() bool {
	return r == RoleClaimant || r == RoleOfficer || r == RoleSupervisor
}

func (r Role) Operational() bool {
	return r == RoleOfficer || r == RoleSupervisor
}

type Principal struct {
	UserID string
	Role   Role
}

func (p Principal) Validate() error {
	if strings.TrimSpace(p.UserID) == "" {
		return FieldError{Field: "user_id", Message: "is required"}
	}
	if !p.Role.Valid() {
		return FieldError{Field: "role", Message: "is invalid"}
	}
	return nil
}

var idNumberPattern = regexp.MustCompile(`^[0-9X]{8,24}$`)

type PersonIdentity struct {
	Name     string
	IDNumber string
}

func (p PersonIdentity) Validate() error {
	name := strings.TrimSpace(p.Name)
	if utf8.RuneCountInString(name) < 2 || utf8.RuneCountInString(name) > 80 {
		return FieldError{Field: "name", Message: "must contain 2 to 80 characters"}
	}
	id := strings.ToUpper(strings.TrimSpace(p.IDNumber))
	if !idNumberPattern.MatchString(id) {
		return FieldError{Field: "id_number", Message: "has invalid format"}
	}
	return nil
}

func (p PersonIdentity) Fingerprint() string {
	normalized := strings.ToUpper(strings.TrimSpace(p.Name)) + "\x00" + strings.ToUpper(strings.TrimSpace(p.IDNumber))
	digest := sha256.Sum256([]byte(normalized))
	return hex.EncodeToString(digest[:])
}

func MaskIDNumber(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= 6 {
		return strings.Repeat("*", len(value))
	}
	return value[:3] + strings.Repeat("*", len(value)-6) + value[len(value)-3:]
}
