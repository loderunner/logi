package ingest

import (
	"bytes"
	"encoding/json/jsontext"
	"errors"
	"io"
	"strings"
	"unicode"
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
// In vals, a string is only its words, so "a\tb  c" is "a b c". Punctuation
// stays. A number or boolean is its literal text, and an array is its compact
// JSON text.
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
// JSON object as an error. It knows about keys, nesting and prefixes, and
// leaves the text of the values to set.
func (f *flattener) parse(line []byte) (string, map[string]string, error) {
	f.reader.Reset(line)
	f.dec.Reset(&f.reader, f.opts...)

	if f.dec.PeekKind() != jsontext.KindBeginObject {
		return "", nil, errNotObject
	}

	var (
		vals   strings.Builder
		fields = make(map[string]string)
		// prefixes[i] is the key prefix inside the i-th open object, such as
		// "http." inside "http".
		prefixes = []string{""}
	)

	_, err := f.dec.ReadToken() // the opening brace
	if err != nil {
		return "", nil, err
	}

	for len(prefixes) > 0 {
		if f.dec.PeekKind() == jsontext.KindEndObject {
			_, err = f.dec.ReadToken()
			if err != nil {
				return "", nil, err
			}

			prefixes = prefixes[:len(prefixes)-1]

			continue
		}

		// Inside an object, a token that isn't a closing brace is a name.
		name, err := f.dec.ReadToken()
		if err != nil {
			return "", nil, err
		}

		key := prefixes[len(prefixes)-1] + name.String()

		if f.dec.PeekKind() == jsontext.KindBeginObject {
			_, err = f.dec.ReadToken()
			if err != nil {
				return "", nil, err
			}

			prefixes = append(prefixes, key+".")

			continue
		}

		err = f.set(fields, &vals, key)
		if err != nil {
			return "", nil, err
		}
	}

	// Only whitespace may follow the object.
	_, err = f.dec.ReadToken()
	if !errors.Is(err, io.EOF) {
		return "", nil, errNotObject
	}

	return vals.String(), fields, nil
}

// set reads the next value, which isn't an object, and records it under key.
// What goes in fields and what goes in vals differ for two kinds of value:
//
//   - A string is its decoded text in fields, and only its words in vals, so
//     that whitespace and control characters don't get in the way of a
//     search.
//   - An array is compact JSON text in both. set doesn't look inside it.
//
// A number or boolean is its literal text in both, and a null is in neither.
func (f *flattener) set(
	fields map[string]string,
	vals *strings.Builder,
	key string,
) error {
	if f.dec.PeekKind() == jsontext.KindBeginArray {
		value, err := f.dec.ReadValue()
		if err != nil {
			return err
		}

		// Compact works in place, and value is the decoder's own buffer.
		compact := value.Clone()

		err = compact.Compact(f.opts...)
		if err != nil {
			return err
		}

		text := string(compact)
		fields[key] = text

		appendVals(vals, text)

		return nil
	}

	tok, err := f.dec.ReadToken()
	if err != nil {
		return err
	}

	switch tok.Kind() {
	case jsontext.KindNull:
	case jsontext.KindString:
		text := tok.String()
		fields[key] = text

		appendWords(vals, text)
	default:
		text := tok.String()
		fields[key] = text

		appendVals(vals, text)
	}

	return nil
}

// appendVals adds text to vals after a space. An empty text adds nothing.
func appendVals(vals *strings.Builder, text string) {
	if text == "" {
		return
	}

	if vals.Len() > 0 {
		vals.WriteByte(' ')
	}

	vals.WriteString(text)
}

// appendWords adds the words of text to vals, each after a space. A word is a
// run of printable characters, punctuation included. Every run of spaces and
// unprintable characters, such as tabs and newlines, is one separator, and it
// is dropped at the ends.
func appendWords(vals *strings.Builder, text string) {
	// A separator is written just before the next word character, so that
	// none is left dangling.
	separate := vals.Len() > 0

	for _, r := range text {
		if r == ' ' || !unicode.IsPrint(r) {
			separate = vals.Len() > 0

			continue
		}

		if separate {
			vals.WriteByte(' ')

			separate = false
		}

		vals.WriteRune(r)
	}
}
