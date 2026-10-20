package sourceparse

import (
	"errors"
	"io"
	"os"
	"unicode/utf8"
)

// ReadText shares bounded text decoding; callers own file identity,
// existence, opening policy and operation-specific capacity diagnostics.
func ReadText(file *os.File, limit int, invalid string) (string, error) {
	data, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if err != nil {
		return "", err
	}
	if len(data) > limit || !utf8.Valid(data) {
		return "", errors.New(invalid)
	}
	return string(data), nil
}
