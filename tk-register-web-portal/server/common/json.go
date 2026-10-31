package common

import (
	"encoding/json"
	"errors"
	"io"
)

func Marshal(v any) ([]byte, error)   { return json.Marshal(v) }
func Unmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }
func DecodeJson(r io.Reader, v any) error {
	d := json.NewDecoder(r)
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("one JSON object is required")
	}
	return nil
}
