package goja

import (
	"math"
	"reflect"
	"testing"
)

func TestToPropertyKeyPrimitiveValues(t *testing.T) {
	tests := []struct {
		name string
		key  Value
	}{
		{name: "ascii string", key: asciiString("plain")},
		{name: "unicode string", key: newStringValue("ключ")},
		{name: "integer", key: valueInt(7)},
		{name: "float", key: valueFloat(1.5)},
		{name: "negative zero", key: valueFloat(math.Copysign(0, -1))},
		{name: "NaN", key: valueFloat(math.NaN())},
		{name: "positive infinity", key: valueFloat(math.Inf(1))},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := toPropertyKey(test.key)
			if gotType, keyType := reflect.TypeOf(got), reflect.TypeOf(test.key); gotType != keyType {
				t.Fatalf("unexpected result type: got %v, want %v", gotType, keyType)
			}
			if got.String() != test.key.String() {
				t.Fatalf("unexpected string form: got %q, want %q", got.String(), test.key.String())
			}
		})
	}

	const SCRIPT = `
	var o = {};
	o["plain"] = 1;
	o["ключ"] = 2;
	o[7] = 3;
	o[1.5] = 4;
	o[-0] = 5;
	o[NaN] = 6;
	o[Infinity] = 7;

	assert.sameValue(o.plain, 1);
	assert.sameValue(o["ключ"], 2);
	assert.sameValue(o["7"], 3);
	assert.sameValue(o["1.5"], 4);
	assert.sameValue(o["0"], 5);
	assert.sameValue(o["NaN"], 6);
	assert.sameValue(o["Infinity"], 7);
	`
	testScriptWithTestLib(SCRIPT, _undefined, t)
}

var benchmarkPropertyKeyResult Value

// BenchmarkToPropertyKeyPrimitive isolates conversion of the primitive key
// representations used by computed property operations. The keys are created
// before timing, and the result is retained so the conversion cannot be
// optimised away.
func BenchmarkToPropertyKeyPrimitive(b *testing.B) {
	tests := []struct {
		name string
		key  Value
	}{
		{name: "ASCIIString", key: asciiString("property")},
		{name: "UnicodeString", key: newStringValue("ключ")},
		{name: "Integer", key: valueInt(17)},
		{name: "Float", key: valueFloat(17.25)},
	}

	for _, test := range tests {
		b.Run(test.name, func(b *testing.B) {
			b.ReportAllocs()
			key := test.key
			var result Value
			for i := 0; i < b.N; i++ {
				result = toPropertyKey(key)
			}
			benchmarkPropertyKeyResult = result
		})
	}
}

// BenchmarkComputedPropertyKeys measures a representative steady-state VM
// workload that repeatedly reads an array with integer computed keys and then
// reads an object with ASCII, Unicode, integer and non-integral numeric keys.
// Runtime setup and compilation are excluded from the timed section.
func BenchmarkComputedPropertyKeys(b *testing.B) {
	b.ReportAllocs()
	vm := New()
	if _, err := vm.RunString(`
		var propertyKeyObject = {
			"plain": 1,
			"ключ": 2,
			"7": 3,
			"1.5": 4
		};
		var propertyKeys = ["plain", "ключ", 7, 1.5];
	`); err != nil {
		b.Fatal(err)
	}
	prg := MustCompile("property_keys.js", `
		var propertyKeyTotal = 0;
		for (var propertyKeyIndex = 0; propertyKeyIndex < 1000; propertyKeyIndex++) {
			propertyKeyTotal += propertyKeyObject[propertyKeys[propertyKeyIndex & 3]];
		}
		propertyKeyTotal;
	`, false)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		result, err := vm.RunProgram(prg)
		if err != nil {
			b.Fatal(err)
		}
		if got := result.ToInteger(); got != 2500 {
			b.Fatalf("unexpected result: got %d, want 2500", got)
		}
	}
}
