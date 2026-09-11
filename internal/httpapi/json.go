package httpapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// decodeJSON reads exactly one JSON value. Checking for EOF after the first decode
// keeps a valid object followed by a second document or trailing garbage from being
// accepted merely because the first object was well formed.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return err
	}

	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		if err != nil {
			return fmt.Errorf("request body must contain one JSON value: %w", err)
		}
		return fmt.Errorf("request body must contain one JSON value")
	}
	return nil
}
