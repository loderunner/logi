package ingest

import (
	"bytes"
	"encoding/json/jsontext"
	"errors"
	"io"
	"strings"
)

// RawKey is the key of a line that isn't a JSON object. Its value is the
// whole line.
const RawKey = "_raw"

// flattener turns a JSON line into flat fields. It reuses its decoder between
// lines, so it isn't safe for concurrent use.
type flattener struct {
	dec    *jsontext.Decoder
	reader bytes.Reader
	opts   []jsontext.Options
}

// newFlattener returns a flattener that accepts the two things real logs get
// wrong: invalid UTF-8 and duplicate keys.
func newFlattener() *flattener {
	return &flattener{
		dec: new(jsontext.Decoder),
		opts: []jsontext.Options{
			jsontext.AllowInvalidUTF8(true),
			jsontext.AllowDuplicateNames(true),
		},
	}
}

// flatten parses line as one JSON object and returns its fields and vals,
// the values joined with spaces in document order, for free-text search.
//
// Nested objects become dotted keys, so {"http":{"status":200}} has the
// field "http.status". A string is its decoded text, a number or boolean is
// its literal text, and an array is compact JSON text. A null and an empty
// object have no value, so they have no field. When a key repeats, the last
// value wins in fields, and vals keeps them all.
//
// A line that isn't valid JSON, isn't an object, or has anything after the
// object has a single field, "_raw", holding the whole line, and that line
// is also vals. It never has partial fields.
func (f *flattener) flatten(line []byte) (string, map[string]string) {
	vals, fields, err := f.parse(line)
	if err != nil {
		text := string(line)

		return text, map[string]string{RawKey: text}
	}

	return vals, fields
}

var errNotObject = errors.New("not a JSON object")

// parse does the work of flatten. It reports any line that isn't exactly one
// JSON object as an error.
func (f *flattener) parse(line []byte) (string, map[string]string, error) {
	f.reader.Reset(line)
	f.dec.Reset(&f.reader, f.opts...)

	if f.dec.PeekKind() != '{' {
		return "", nil, errNotObject
	}

	var (
		vals   strings.Builder
		fields = make(map[string]string)
		// prefixes[i] is the key prefix inside the i-th open object, such as
		// "http." inside "http".
		prefixes = []string{""}
	)

	set := func(key, value string) {
		fields[key] = value

		if value == "" {
			return
		}

		if vals.Len() > 0 {
			vals.WriteByte(' ')
		}

		vals.WriteString(value)
	}

	_, err := f.dec.ReadToken() // the opening brace
	if err != nil {
		return "", nil, err
	}

	for len(prefixes) > 0 {
		tok, err := f.dec.ReadToken()
		if err != nil {
			return "", nil, err
		}

		prefix := prefixes[len(prefixes)-1]

		if tok.Kind() == '}' {
			prefixes = prefixes[:len(prefixes)-1]
			continue
		}

		// Inside an object, a token that isn't a closing brace is a name.
		key := prefix + tok.String()

		switch f.dec.PeekKind() {
		case '{':
			_, err = f.dec.ReadToken()
			if err != nil {
				return "", nil, err
			}

			prefixes = append(prefixes, key+".")
		case '[':
			value, err := f.dec.ReadValue()
			if err != nil {
				return "", nil, err
			}

			compact := value.Clone()

			err = compact.Compact(f.opts...)
			if err != nil {
				return "", nil, err
			}

			set(key, string(compact))
		default:
			value, err := f.dec.ReadToken()
			if err != nil {
				return "", nil, err
			}

			if value.Kind() != 'n' {
				set(key, value.String())
			}
		}
	}

	// Only whitespace may follow the object.
	_, err = f.dec.ReadToken()
	if !errors.Is(err, io.EOF) {
		return "", nil, errNotObject
	}

	return vals.String(), fields, nil
}
