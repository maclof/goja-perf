package goja

import "testing"

func BenchmarkLiteralCompilation(b *testing.B) {
	for _, test := range []struct {
		name   string
		source string
	}{
		{"ObjectEight", `({a: 1, b: 2, c: 3, d: 4, e: 5, f: 6, g: 7, h: 8})`},
		{"ArrayEight", `[1, 2, 3, 4, 5, 6, 7, 8]`},
	} {
		b.Run(test.name, func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				program, err := Compile("literal.js", test.source, false)
				if err != nil || program == nil {
					b.Fatalf("compile failed: %v", err)
				}
			}
		})
	}
}

func BenchmarkLiteralConstruction(b *testing.B) {
	tests := []struct {
		name   string
		source string
		check  func(*Object) bool
	}{
		{"ObjectEmpty", `({})`, func(object *Object) bool { return !object.self.hasOwnPropertyStr("a") }},
		{"ObjectTwo", `({a: 1, b: 2})`, func(object *Object) bool {
			return object.Get("a").ToInteger() == 1 && object.Get("b").ToInteger() == 2
		}},
		{"ObjectEight", `({a: 1, b: 2, c: 3, d: 4, e: 5, f: 6, g: 7, h: 8})`, func(object *Object) bool {
			return object.Get("a").ToInteger()+object.Get("h").ToInteger() == 9
		}},
		{"ArrayEmpty", `[]`, func(object *Object) bool { return object.Get("length").ToInteger() == 0 }},
		{"ArrayTwo", `[1, 2]`, func(object *Object) bool {
			return object.Get("0").ToInteger()+object.Get("1").ToInteger() == 3
		}},
		{"ArrayEight", `[1, 2, 3, 4, 5, 6, 7, 8]`, func(object *Object) bool {
			return object.Get("0").ToInteger()+object.Get("7").ToInteger() == 9
		}},
	}
	for _, test := range tests {
		b.Run(test.name, func(b *testing.B) {
			runtime := New()
			value, err := runtime.RunString(`(function() { return ` + test.source + `; })`)
			if err != nil {
				b.Fatal(err)
			}
			call, ok := AssertFunction(value)
			if !ok {
				b.Fatal("literal factory is not callable")
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				result, err := call(_undefined)
				if err != nil {
					b.Fatal(err)
				}
				object, ok := result.(*Object)
				if !ok || !test.check(object) {
					b.Fatalf("unexpected result: %v", result)
				}
			}
		})
	}
}
