package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
)

const maxJSONBody = 1 << 20

func decodeJSON(w http.ResponseWriter, r *http.Request, destination any) error {
	r.Body = http.MaxBytesReader(w, r.Body, maxJSONBody)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		var syntax *json.SyntaxError
		var typeError *json.UnmarshalTypeError
		switch {
		case errors.As(err, &syntax):
			return fmt.Errorf("request body contains malformed JSON near byte %d", syntax.Offset)
		case errors.Is(err, io.ErrUnexpectedEOF):
			return errors.New("request body contains malformed JSON")
		case errors.Is(err, io.EOF):
			return errors.New("request body is required")
		case errors.As(err, &typeError):
			return fmt.Errorf("request field %s has the wrong type", typeError.Field)
		case strings.HasPrefix(err.Error(), "json: unknown field "):
			return fmt.Errorf("request body contains %s", strings.TrimPrefix(err.Error(), "json: "))
		default:
			return fmt.Errorf("decode request body: %w", err)
		}
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return errors.New("request body must contain one JSON value")
	}
	return nil
}

func parseVersion(r *http.Request) (int64, error) {
	value := strings.TrimSpace(r.Header.Get("If-Match"))
	value = strings.Trim(value, `"`)
	if value == "" {
		return 0, errors.New("If-Match version header is required")
	}
	version, err := strconv.ParseInt(value, 10, 64)
	if err != nil || version < 1 {
		return 0, errors.New("If-Match must contain a positive integer version")
	}
	return version, nil
}
