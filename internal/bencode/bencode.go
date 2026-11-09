package bencode

import (
	"bytes"
	"errors"
	"fmt"
	"sort"
	"strconv"
)

var errEnd = errors.New("bencode: unexpected end of data")

// ---------- encoding ----------

func Encode(v interface{}) ([]byte, error) {
	var b bytes.Buffer
	if err := enc(&b, v); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

func enc(b *bytes.Buffer, v interface{}) error {
	switch x := v.(type) {
	case int:
		fmt.Fprintf(b, "i%de", x)
	case int64:
		fmt.Fprintf(b, "i%de", x)
	case string:
		fmt.Fprintf(b, "%d:%s", len(x), x)
	case []byte:
		fmt.Fprintf(b, "%d:", len(x))
		b.Write(x)
	case []interface{}:
		b.WriteByte('l')
		for _, e := range x {
			if err := enc(b, e); err != nil {
				return err
			}
		}
		b.WriteByte('e')
	case map[string]interface{}:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		sort.Strings(keys) // canonical order: required for a correct info hash
		b.WriteByte('d')
		for _, k := range keys {
			fmt.Fprintf(b, "%d:%s", len(k), k)
			if err := enc(b, x[k]); err != nil {
				return err
			}
		}
		b.WriteByte('e')
	default:
		return fmt.Errorf("bencode: unsupported type %T", v)
	}
	return nil
}

// ---------- decoding ----------

type decoder struct {
	data []byte
	pos  int
}

func Decode(data []byte) (interface{}, error) {
	d := &decoder{data: data}
	v, err := d.next()
	if err != nil {
		return nil, err
	}
	if d.pos != len(data) {
		return nil, errors.New("bencode: trailing data")
	}
	return v, nil
}

func (d *decoder) next() (interface{}, error) {
	if d.pos >= len(d.data) {
		return nil, errEnd
	}
	switch c := d.data[d.pos]; {
	case c == 'i':
		return d.integer()
	case c == 'l':
		d.pos++
		list := []interface{}{}
		for {
			if d.pos >= len(d.data) {
				return nil, errEnd
			}
			if d.data[d.pos] == 'e' {
				d.pos++
				return list, nil
			}
			v, err := d.next()
			if err != nil {
				return nil, err
			}
			list = append(list, v)
		}
	case c == 'd':
		d.pos++
		m := map[string]interface{}{}
		for {
			if d.pos >= len(d.data) {
				return nil, errEnd
			}
			if d.data[d.pos] == 'e' {
				d.pos++
				return m, nil
			}
			k, err := d.str()
			if err != nil {
				return nil, fmt.Errorf("bencode: bad dict key: %w", err)
			}
			v, err := d.next()
			if err != nil {
				return nil, err
			}
			m[k] = v
		}
	case c >= '0' && c <= '9':
		return d.str()
	default:
		return nil, fmt.Errorf("bencode: unexpected byte %q at %d", c, d.pos)
	}
}

func (d *decoder) integer() (interface{}, error) {
	end := bytes.IndexByte(d.data[d.pos:], 'e')
	if end < 0 {
		return nil, errEnd
	}
	s := string(d.data[d.pos+1 : d.pos+end])
	// reject "", "-0", and leading zeros like "03"
	if s == "" || s == "-0" || (len(s) > 1 && s[0] == '0') || (len(s) > 2 && s[:2] == "-0") {
		return nil, fmt.Errorf("bencode: invalid integer %q", s)
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return nil, err
	}
	d.pos += end + 1
	return n, nil
}

func (d *decoder) str() (string, error) {
	colon := bytes.IndexByte(d.data[d.pos:], ':')
	if colon < 0 {
		return "", errEnd
	}
	n, err := strconv.Atoi(string(d.data[d.pos : d.pos+colon]))
	if err != nil || n < 0 {
		return "", errors.New("bencode: invalid string length")
	}
	start := d.pos + colon + 1
	if start+n > len(d.data) {
		return "", errEnd
	}
	d.pos = start + n
	return string(d.data[start : start+n]), nil
}

// DictValueSpan returns the original, unmodified bytes of the value stored
// under key in the top-level dictionary of data.
func DictValueSpan(data []byte, key string) ([]byte, error) {
	d := &decoder{data: data}
	if len(data) == 0 || data[0] != 'd' {
		return nil, errors.New("bencode: not a dict")
	}
	d.pos++
	for {
		if d.pos >= len(d.data) {
			return nil, errEnd
		}
		if d.data[d.pos] == 'e' {
			return nil, fmt.Errorf("bencode: key %q not found", key)
		}
		k, err := d.str()
		if err != nil {
			return nil, err
		}
		start := d.pos
		if _, err := d.next(); err != nil { // advances d.pos past the value
			return nil, err
		}
		if k == key {
			return data[start:d.pos], nil
		}
	}
}
