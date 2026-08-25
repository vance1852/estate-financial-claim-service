package domain

import (
	"errors"
	"fmt"
)

var (
	ErrValidation   = errors.New("validation failed")
	ErrNotFound     = errors.New("not found")
	ErrConflict     = errors.New("conflict")
	ErrUnauthorized = errors.New("unauthorized")
	ErrForbidden    = errors.New("forbidden")
	ErrExpired      = errors.New("expired")
	ErrInvalidState = errors.New("invalid state transition")
	ErrDependency   = errors.New("dependency failure")
)

type FieldError struct {
	Field   string
	Message string
}

func (e FieldError) Error() string {
	return fmt.Sprintf("%s: %s", e.Field, e.Message)
}

func (e FieldError) Unwrap() error { return ErrValidation }

type VersionConflict struct {
	Entity   string
	ID       string
	Expected int64
}

func (e VersionConflict) Error() string {
	return fmt.Sprintf("%s %s version %d is stale", e.Entity, e.ID, e.Expected)
}

func (e VersionConflict) Unwrap() error { return ErrConflict }

type StateError struct {
	Entity string
	From   string
	To     string
}

func (e StateError) Error() string {
	return fmt.Sprintf("cannot transition %s from %s to %s", e.Entity, e.From, e.To)
}

func (e StateError) Unwrap() error { return ErrInvalidState }
