package server

import (
	"reflect"
	"strings"
	"time"
)

// PublicError is the structural contract used by feature blocks to request a
// stable, safe HTTP error without importing the server package. Error should
// retain the internal cause for logs; PublicMessage is the only message exposed
// to the client.
type PublicError interface {
	error
	HTTPStatus() int
	PublicCode() string
	PublicMessage() string
	RetryAfter() time.Duration
}

type publicErrorMetadata struct {
	status     int
	code       string
	message    string
	retryAfter time.Duration
}

func readPublicError(err PublicError) (metadata publicErrorMetadata, ok bool) {
	defer func() {
		if recover() != nil {
			metadata, ok = publicErrorMetadata{}, false
		}
	}()
	if isNilError(err) {
		return publicErrorMetadata{}, false
	}
	metadata = publicErrorMetadata{
		status: err.HTTPStatus(), code: err.PublicCode(), message: err.PublicMessage(), retryAfter: err.RetryAfter(),
	}
	if metadata.status < 400 || metadata.status > 599 {
		return publicErrorMetadata{}, false
	}
	code := metadata.code
	if len(code) == 0 || len(code) > 64 || code[0] < 'a' || code[0] > 'z' {
		return publicErrorMetadata{}, false
	}
	for i := 1; i < len(code); i++ {
		c := code[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' {
			return publicErrorMetadata{}, false
		}
	}
	message := metadata.message
	if message == "" || len(message) > 200 || strings.IndexFunc(message, func(r rune) bool {
		return r < 0x20 || r == 0x7f
	}) >= 0 {
		return publicErrorMetadata{}, false
	}
	retry := metadata.retryAfter
	if retry < 0 || (retry != 0 && metadata.status != 429 && metadata.status != 503) {
		return publicErrorMetadata{}, false
	}
	return metadata, true
}

func isNilError(err error) bool {
	if err == nil {
		return true
	}
	v := reflect.ValueOf(err)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Ptr, reflect.Slice:
		return v.IsNil()
	default:
		return false
	}
}

func retryAfterSeconds(duration time.Duration) int64 {
	if duration <= 0 {
		return 0
	}
	seconds := duration / time.Second
	if duration%time.Second != 0 {
		seconds++
	}
	return int64(seconds)
}
