package goja_test

import (
	"fmt"

	goja "github.com/maclof/goja-perf"
)

func ExampleNew() {
	vm := goja.New()
	value, err := vm.RunString(`({ answer: 6 * 7 }).answer`)
	if err != nil {
		panic(err)
	}

	fmt.Println(value.ToInteger())
	// Output: 42
}

func ExampleNewSandbox() {
	sandbox, err := goja.NewSandbox(goja.SandboxPolicy{
		Builtins: goja.SandboxBuiltins{
			Allow: []string{"Math"},
		},
		Capabilities: map[string]interface{}{
			"double": func(value int64) int64 { return value * 2 },
		},
	})
	if err != nil {
		panic(err)
	}

	value, err := sandbox.RunString(`Math.max(double(21), 0)`)
	if err != nil {
		panic(err)
	}

	fmt.Println(value.ToInteger())
	// Output: 42
}
