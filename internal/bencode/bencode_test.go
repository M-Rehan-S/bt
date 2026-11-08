package bencode

import (
	"reflect"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	cases := []string{
		"i42e", "i-7e", "i0e", "4:spam", "0:",
		"l4:spami42ee", "le", "de",
		"d3:cow3:moo4:spam4:eggse",
		"d4:infod6:lengthi10e4:name1:xee",
	}
	for _, c := range cases {
		v, err := Decode([]byte(c))
		if err != nil {
			t.Fatalf("decode %q: %v", c, err)
		}
		out, err := Encode(v)
		if err != nil {
			t.Fatalf("encode %q: %v", c, err)
		}
		if string(out) != c {
			t.Errorf("round trip %q -> %q", c, out)
		}
	}
}

func TestInvalid(t *testing.T) {
	bad := []string{"i-0e", "i03e", "ie", "5:abc", "i42", "l", "d3:cow", "x", "i1ei2e"}
	for _, c := range bad {
		if _, err := Decode([]byte(c)); err == nil {
			t.Errorf("expected error for %q", c)
		}
	}
}

func TestSortedKeys(t *testing.T) {
	out, _ := Encode(map[string]interface{}{"b": int64(1), "a": int64(2)})
	if string(out) != "d1:ai2e1:bi1ee" {
		t.Errorf("keys not sorted: %s", out)
	}
}

func TestDecodeTypes(t *testing.T) {
	v, _ := Decode([]byte("d1:ai5e1:bl1:xee"))
	want := map[string]interface{}{"a": int64(5), "b": []interface{}{"x"}}
	if !reflect.DeepEqual(v, want) {
		t.Errorf("got %#v", v)
	}
}
