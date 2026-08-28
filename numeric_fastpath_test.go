package goja

import (
	"math"
	"testing"
)

// TestFloatArithmeticFastPath pins the observable semantics of the
// valueFloat*valueFloat fast paths in _mul, _add and _or: results must be
// identical to the general paths, including canonicalization of integral
// results to valueInt (observable via Export() as int64), negative zero,
// NaN/Infinity propagation, ECMAScript ToInt32 semantics for bitwise OR,
// BigInt type errors, string/object coercion and exceptions.
//
// All inputs are runtime variable values, so no compiler constant folding can
// bypass the VM opcodes under test.
func TestFloatArithmeticFastPath(t *testing.T) {
	rt := New()
	v, err := rt.RunString(`
	var f01 = 0.1, f02 = 0.2, f03 = 0.3, q = 1.5, two = 2.0, half = 2.5,
	    f37 = 3.7, fn37 = -3.7, f49 = 4.9, f21 = 2.1, fn05 = -0.5,
	    z = 0.0, m1 = -1.0, nz = -0.0, big = 1e308, tiny = 5e-324,
	    nan = NaN, pinf = Infinity, ninf = -Infinity,
	    big31 = 2147483648.5, big32 = 4294967296.5, e100 = 1e100,
	    ne100 = -1e100, seven = 7.5;
	var r = {};
	// _mul fast path (both operands unboxed doubles)
	r.mul_nonint = f01 * f03;
	r.mul_canon = q * two;
	r.mul_canon_gen = q * 2;
	r.mul_negzero = z * m1;
	r.mul_negzero2 = nz * half;
	r.mul_nan = nan * two;
	r.mul_pinf = big * 10.0;
	r.mul_ninf = -big * 10.0;
	r.mul_tiny = tiny * 2.0;
	r.mul_eq = (f01 * f03) === 0.03;
	// _add fast path
	r.add_repr = f01 + f02;
	r.add_canon = half + half;
	r.add_negzero = nz + nz;
	r.add_nan = nan + q;
	r.add_pinf = big + big;
	r.add_ninf = ninf + ninf;
	// _or fast path (ECMAScript ToInt32 of unboxed doubles)
	r.or_trunc = f37 | z;
	r.or_trunc_neg = fn37 | z;
	r.or_nan = nan | z;
	r.or_pinf = pinf | z;
	r.or_ninf = ninf | z;
	r.or_wrap31 = big31 | z;
	r.or_wrap31_gen = big31 | 0;
	r.or_wrap32 = big32 | z;
	r.or_both = f49 | f21;
	r.or_neghalf = fn05 | z;
	r.or_huge = e100 | z;
	r.or_huge_gen = e100 | 0;
	r.or_neghuge = ne100 | z;
	r.or_canon = seven | seven;
	r.or_gen_int = 5 | 2;
	// mixed operand types must keep using the general paths
	r.mix_mul_int = half * 2;
	r.mix_add_int = f01 + 1;
	r.mix_str1 = "a" + q;
	r.mix_str2 = half + "x";
	r.mix_obj = { valueOf: function () { return 2.5; } } * 2;
	r.typeof_mul = typeof (q * two);
	r.strict_eq = (q * two) === 3;
	// BigInt mixing and exceptions must be unaffected
	r.bi_mul = (function () { try { q * (2n); return "no-throw"; } catch (e) { return e.constructor.name; } })();
	r.bi_or = (function () { try { (2n) | q; return "no-throw"; } catch (e) { return e.constructor.name; } })();
	r.throw_obj = (function () {
		try {
			var bad = { valueOf: function () { throw new RangeError("boom"); } };
			return bad * two;
		} catch (e) { return e.constructor.name; }
	})();
	r
	`)
	if err != nil {
		t.Fatal(err)
	}
	obj, ok := v.(*Object)
	if !ok {
		t.Fatalf("script did not return an object: %v", v)
	}

	tiny := float64(5e-324)
	cases := []struct {
		key      string
		check    func(x interface{}) bool
		expected string
	}{
		{"mul_nonint", exactFloat(float64(0.1) * float64(0.3)), "float64"},
		{"mul_canon", exactInt64(3), "int64 (fast-path canonicalization)"},
		{"mul_canon_gen", exactInt64(3), "int64"},
		{"mul_negzero", negZero, "-0"},
		{"mul_negzero2", negZero, "-0"},
		{"mul_nan", nan, "NaN"},
		{"mul_pinf", inf(1), "+Inf"},
		{"mul_ninf", inf(-1), "-Inf"},
		{"mul_tiny", exactFloat(tiny * 2), "float64"},
		{"mul_eq", exactBool(true), "true"},
		{"add_repr", exactFloat(float64(0.1) + float64(0.2)), "float64"},
		{"add_canon", exactInt64(5), "int64 (fast-path canonicalization)"},
		{"add_negzero", negZero, "-0"},
		{"add_nan", nan, "NaN"},
		{"add_pinf", inf(1), "+Inf"},
		{"add_ninf", inf(-1), "-Inf"},
		{"or_trunc", exactInt64(3), "int64"},
		{"or_trunc_neg", exactInt64(-3), "int64"},
		{"or_nan", exactInt64(0), "int64"},
		{"or_pinf", exactInt64(0), "int64"},
		{"or_ninf", exactInt64(0), "int64"},
		{"or_wrap31", exactInt64(-2147483648), "int64"},
		{"or_wrap31_gen", exactInt64(-2147483648), "int64"},
		{"or_wrap32", exactInt64(0), "int64"},
		{"or_both", exactInt64(6), "int64"},
		{"or_neghalf", exactInt64(0), "int64"},
		{"or_huge", exactInt64(0), "int64"},
		{"or_huge_gen", exactInt64(0), "int64"},
		{"or_neghuge", exactInt64(0), "int64"},
		{"or_canon", exactInt64(7), "int64"},
		{"or_gen_int", exactInt64(7), "int64"},
		{"mix_mul_int", exactInt64(5), "int64"},
		{"mix_add_int", exactFloat(float64(0.1) + 1), "float64"},
		{"mix_str1", exactString("a1.5"), `"a1.5"`},
		{"mix_str2", exactString("2.5x"), `"2.5x"`},
		{"mix_obj", exactInt64(5), "int64"},
		{"typeof_mul", exactString("number"), `"number"`},
		{"strict_eq", exactBool(true), "true"},
		{"bi_mul", exactString("TypeError"), `"TypeError"`},
		{"bi_or", exactString("TypeError"), `"TypeError"`},
		{"throw_obj", exactString("RangeError"), `"RangeError"`},
	}

	for _, c := range cases {
		got := obj.Get(c.key).Export()
		if !c.check(got) {
			t.Errorf("r.%s = %#v, want %s", c.key, got, c.expected)
		}
	}
}

func exactInt64(want int64) func(interface{}) bool {
	return func(x interface{}) bool {
		i, ok := x.(int64)
		return ok && i == want
	}
}

func exactFloat(want float64) func(interface{}) bool {
	return func(x interface{}) bool {
		f, ok := x.(float64)
		return ok && f == want
	}
}

func exactString(want string) func(interface{}) bool {
	return func(x interface{}) bool {
		s, ok := x.(string)
		return ok && s == want
	}
}

func exactBool(want bool) func(interface{}) bool {
	return func(x interface{}) bool {
		b, ok := x.(bool)
		return ok && b == want
	}
}

func nan(x interface{}) bool {
	f, ok := x.(float64)
	return ok && math.IsNaN(f)
}

func inf(sign int) func(interface{}) bool {
	return func(x interface{}) bool {
		f, ok := x.(float64)
		return ok && math.IsInf(f, sign)
	}
}

func negZero(x interface{}) bool {
	f, ok := x.(float64)
	return ok && f == 0 && math.Signbit(f)
}
