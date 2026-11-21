package composer

import (
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// FieldStream pulls the text of chosen string fields out of a JSON document
// while it is still streaming, so the UI can fill each message box live
// instead of showing raw JSON.
//
// Chunks may split the document anywhere: inside a key, an escape sequence or a
// multi-byte character. Fields are named by their path of object keys joined
// with "." (e.g. "sms", or "email.subject" for nested objects). Strings inside
// arrays are not reported.
type FieldStream struct {
	fields map[string]bool
	emit   func(field, text string)

	stack []frame
	carry []byte // an incomplete UTF-8 character from the previous chunk

	inString bool
	isKey    bool
	keyBuf   strings.Builder
	field    string // the field the current string value belongs to, or ""

	escape   bool
	hex      []rune // digits of a \uXXXX escape being read
	surrHigh rune   // a high surrogate waiting for its low half

	outField string
	out      strings.Builder
}

type frame struct {
	object    bool
	expectKey bool
	key       string
}

func NewFieldStream(fields []string, emit func(field, text string)) *FieldStream {
	f := &FieldStream{fields: map[string]bool{}, emit: emit}
	for _, name := range fields {
		f.fields[name] = true
	}
	return f
}

// Write feeds the next chunk and emits the field text it contained.
func (f *FieldStream) Write(chunk string) {
	b := append(f.carry, chunk...)
	f.carry = nil
	for len(b) > 0 {
		r, size := utf8.DecodeRune(b)
		if r == utf8.RuneError && size <= 1 && !utf8.FullRune(b) {
			f.carry = append([]byte(nil), b...) // wait for the rest of the character
			break
		}
		f.step(r)
		b = b[size:]
	}
	f.flush()
}

func (f *FieldStream) step(r rune) {
	if f.inString {
		f.stringRune(r)
		return
	}
	top := f.top()
	switch r {
	case '{':
		f.stack = append(f.stack, frame{object: true, expectKey: true})
	case '[':
		f.stack = append(f.stack, frame{})
	case '}', ']':
		if len(f.stack) > 0 {
			f.stack = f.stack[:len(f.stack)-1]
		}
	case ',':
		if top != nil && top.object {
			top.expectKey = true
		}
	case ':':
		if top != nil {
			top.expectKey = false
		}
	case '"':
		f.inString = true
		f.isKey = top != nil && top.object && top.expectKey
		f.keyBuf.Reset()
		f.field = ""
		if !f.isKey {
			if path, ok := f.path(); ok && f.fields[path] {
				f.field = path
			}
		}
	}
}

func (f *FieldStream) stringRune(r rune) {
	switch {
	case f.hex != nil:
		f.hex = append(f.hex, r)
		if len(f.hex) == 4 {
			code := hexValue(f.hex)
			f.hex = nil
			f.unicode(code)
		}
		return
	case f.escape:
		f.escape = false
		switch r {
		case 'u':
			f.hex = make([]rune, 0, 4)
			return
		case 'n':
			r = '\n'
		case 't':
			r = '\t'
		case 'r':
			r = '\r'
		case 'b':
			r = '\b'
		case 'f':
			r = '\f'
		} // '"', '\\' and '/' stand for themselves
		f.char(r)
		return
	case r == '\\':
		f.escape = true
		return
	case r == '"':
		f.inString = false
		if f.isKey {
			if top := f.top(); top != nil {
				top.key = f.keyBuf.String()
			}
		}
		f.field = ""
		return
	}
	f.char(r)
}

func (f *FieldStream) unicode(code rune) {
	switch {
	case utf16.IsSurrogate(code) && code < 0xDC00:
		f.surrHigh = code // wait for the low half
	case utf16.IsSurrogate(code) && f.surrHigh != 0:
		f.char(utf16.DecodeRune(f.surrHigh, code))
		f.surrHigh = 0
	default:
		f.surrHigh = 0
		f.char(code)
	}
}

func (f *FieldStream) char(r rune) {
	if f.isKey {
		f.keyBuf.WriteRune(r)
		return
	}
	if f.field == "" {
		return
	}
	if f.outField != f.field {
		f.flush()
		f.outField = f.field
	}
	f.out.WriteRune(r)
}

func (f *FieldStream) flush() {
	if f.out.Len() > 0 {
		f.emit(f.outField, f.out.String())
		f.out.Reset()
	}
}

func (f *FieldStream) top() *frame {
	if len(f.stack) == 0 {
		return nil
	}
	return &f.stack[len(f.stack)-1]
}

// path is the dotted key path of the value being read; ok is false inside arrays.
func (f *FieldStream) path() (string, bool) {
	keys := make([]string, 0, len(f.stack))
	for _, fr := range f.stack {
		if !fr.object {
			return "", false
		}
		keys = append(keys, fr.key)
	}
	return strings.Join(keys, "."), true
}

func hexValue(digits []rune) rune {
	var v rune
	for _, d := range digits {
		v <<= 4
		switch {
		case d >= '0' && d <= '9':
			v |= d - '0'
		case d >= 'a' && d <= 'f':
			v |= d - 'a' + 10
		case d >= 'A' && d <= 'F':
			v |= d - 'A' + 10
		}
	}
	return v
}
