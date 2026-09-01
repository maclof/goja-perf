package goja

import "testing"

type boundaryBenchRecord struct {
	Count int
	Label string
}

type boundaryBenchHost struct {
	total int64
}

func (h *boundaryBenchHost) Add(value int64) int64 {
	h.total += value
	return h.total
}

func BenchmarkBoundaryJSToGoByShape(b *testing.B) {
	tests := []struct {
		name   string
		fn     interface{}
		source string
		want   int64
	}{
		{"Primitive", func(value int64) int64 { return value + 1 }, "f(41)", 42},
		{"Slice", func(value []int) int { return value[0] + value[1] }, "f([19, 23])", 42},
		{"Map", func(value map[string]int) int { return value["answer"] }, "f({answer: 42})", 42},
		{"Struct", func(value boundaryBenchRecord) int { return value.Count }, "f({Count: 42, Label: 'x'})", 42},
	}
	for _, test := range tests {
		b.Run(test.name, func(b *testing.B) {
			runtime := New()
			if err := runtime.Set("f", test.fn); err != nil {
				b.Fatal(err)
			}
			program := MustCompile("boundary_js_to_go.js", test.source, true)
			b.ReportAllocs()
			b.ResetTimer()
			var sum int64
			for i := 0; i < b.N; i++ {
				result, err := runtime.RunProgram(program)
				if err != nil || result.ToInteger() != test.want {
					b.Fatalf("result=%v err=%v", result, err)
				}
				sum += result.ToInteger()
			}
			if sum != int64(b.N)*test.want {
				b.Fatalf("sum=%d", sum)
			}
		})
	}

}

func BenchmarkBoundaryPrimitiveMethod(b *testing.B) {
	runtime := New()
	host := &boundaryBenchHost{}
	if err := runtime.Set("host", host); err != nil {
		b.Fatal(err)
	}
	program := MustCompile("boundary_js_to_go_method.js", "host.Add(1)", true)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		result, err := runtime.RunProgram(program)
		if err != nil || result.ToInteger() != int64(i+1) {
			b.Fatalf("iteration=%d result=%v err=%v", i, result, err)
		}
	}
}

func BenchmarkBoundaryJSObjectMethod(b *testing.B) {
	runtime := New()
	if _, err := runtime.RunString(`var boundaryObject = { add: function(value) { return value + 1; } };`); err != nil {
		b.Fatal(err)
	}
	program := MustCompile("boundary_js_method.js", "boundaryObject.add(41)", true)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		result, err := runtime.RunProgram(program)
		if err != nil || result.ToInteger() != 42 {
			b.Fatalf("result=%v err=%v", result, err)
		}
	}
}

func BenchmarkBoundaryGoToJSByShape(b *testing.B) {
	tests := []struct {
		name   string
		source string
		arg    func(*Runtime) Value
		want   int64
	}{
		{"Primitive", "function f(value) { return value + 1; }", func(*Runtime) Value { return valueInt(41) }, 42},
		{"Slice", "function f(value) { return value[0] + value[1]; }", func(runtime *Runtime) Value { return runtime.ToValue([]int{19, 23}) }, 42},
		{"Map", "function f(value) { return value.answer; }", func(runtime *Runtime) Value { return runtime.ToValue(map[string]int{"answer": 42}) }, 42},
		{"Struct", "function f(value) { return value.Count; }", func(runtime *Runtime) Value { return runtime.ToValue(boundaryBenchRecord{Count: 42, Label: "x"}) }, 42},
	}
	for _, test := range tests {
		b.Run(test.name, func(b *testing.B) {
			runtime := New()
			if _, err := runtime.RunString(test.source); err != nil {
				b.Fatal(err)
			}
			call, ok := AssertFunction(runtime.Get("f"))
			if !ok {
				b.Fatal("f is not callable")
			}
			argument := test.arg(runtime)
			b.ReportAllocs()
			b.ResetTimer()
			var sum int64
			for i := 0; i < b.N; i++ {
				result, err := call(_undefined, argument)
				if err != nil || result.ToInteger() != test.want {
					b.Fatalf("result=%v err=%v", result, err)
				}
				sum += result.ToInteger()
			}
			if sum != int64(b.N)*test.want {
				b.Fatalf("sum=%d", sum)
			}
		})
	}
}

func BenchmarkBoundaryToValueSetup(b *testing.B) {
	record := boundaryBenchRecord{Count: 42, Label: "x"}
	slice := []int{19, 23}
	valueMap := map[string]int{"answer": 42}
	for _, test := range []struct {
		name  string
		value interface{}
	}{
		{"Slice", slice},
		{"Map", valueMap},
		{"Struct", record},
	} {
		b.Run(test.name, func(b *testing.B) {
			runtime := New()
			b.ReportAllocs()
			b.ResetTimer()
			var result Value
			for i := 0; i < b.N; i++ {
				result = runtime.ToValue(test.value)
			}
			if result == nil {
				b.Fatal("nil result")
			}
		})
	}
}
