package goja

import (
	"bytes"
	stdbase64 "encoding/base64"
	"runtime"
	"strings"
	"testing"
)

var (
	base64BenchmarkRead    int
	base64BenchmarkWritten int
	base64BenchmarkBytes   []byte
	base64BenchmarkValue   Value
)

func addBase64Whitespace(encoded string, lineLength int) asciiString {
	if lineLength <= 0 {
		return asciiString(encoded)
	}
	var result strings.Builder
	result.Grow(len(encoded) + len(encoded)/lineLength*3)
	for len(encoded) > lineLength {
		result.WriteString(encoded[:lineLength])
		result.WriteString(" \r\n")
		encoded = encoded[lineLength:]
	}
	result.WriteString(encoded)
	return asciiString(result.String())
}

func benchmarkBase64EncodedInput(decodedLen int, encoding *stdbase64.Encoding, lineLength int) asciiString {
	encoded := encoding.EncodeToString(bytes.Repeat([]byte{0xa7}, decodedLen))
	return addBase64Whitespace(encoded, lineLength)
}

func TestFromBase64BackingCapacity(t *testing.T) {
	const decodedLen = 4096
	encoded := stdbase64.StdEncoding.EncodeToString(bytes.Repeat([]byte{0xa7}, decodedLen))
	tests := []struct {
		name       string
		input      asciiString
		wantErr    bool
		maxBacking int
	}{
		{name: "clean", input: asciiString(encoded), maxBacking: decodedLen + 2},
		{name: "PEM-whitespace", input: addBase64Whitespace(encoded, 76), maxBacking: decodedLen + 2},
		{name: "dense-whitespace", input: addBase64Whitespace(encoded, 4), maxBacking: decodedLen + 2},
		{name: "whitespace-only", input: asciiString(strings.Repeat(" \t\r\n\f", decodedLen)), maxBacking: 0},
		{name: "invalid-first", input: asciiString("#" + strings.Repeat("A", decodedLen)), wantErr: true, maxBacking: 3},
		{name: "invalid-after-quartet", input: asciiString("AAAA#" + strings.Repeat("A", decodedLen)), wantErr: true, maxBacking: 6},
		{name: "invalid-after-whitespace", input: asciiString(" \nAAAA#" + strings.Repeat("A", decodedLen)), wantErr: true, maxBacking: 6},
		{name: "late-whitespace", input: asciiString(strings.Repeat("A", 128) + strings.Repeat(" ", decodedLen)), maxBacking: 112},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, decoded, err := fromBase64(test.input, &base64DecodeMap, base64LastChunkHandlingLoose)
			if (err != nil) != test.wantErr {
				t.Fatalf("error = %v, wantErr = %v", err, test.wantErr)
			}
			if got := cap(decoded); got > test.maxBacking {
				t.Fatalf("backing capacity = %d, want <= %d (decoded length %d)", got, test.maxBacking, len(decoded))
			}
		})
	}
}

func TestBase64DecodeWhitespaceReadWritten(t *testing.T) {
	tests := []struct {
		name        string
		input       asciiString
		handling    base64LastChunkHandling
		dstLen      int
		wantRead    int
		wantWritten int
		want        string
		wantErr     bool
	}{
		{
			name: "loose", input: " \taGVs \r\nbG8=\n", handling: base64LastChunkHandlingLoose,
			dstLen: 8, wantRead: 14, wantWritten: 5, want: "hello",
		},
		{
			name: "strict", input: " \taGVs \r\nbG8=\n", handling: base64LastChunkHandlingStrict,
			dstLen: 8, wantRead: 14, wantWritten: 5, want: "hello",
		},
		{
			name: "stop-before-partial", input: " \taGVs \r\nbG8\n", handling: base64LastChunkHandlingStop,
			dstLen: 8, wantRead: 6, wantWritten: 3, want: "hel",
		},
		{
			name: "short-three-bytes", input: " \naGVs \tbG8=", handling: base64LastChunkHandlingLoose,
			dstLen: 3, wantRead: 6, wantWritten: 3, want: "hel",
		},
		{
			name: "short-two-bytes", input: " \naGVs \tbG8=", handling: base64LastChunkHandlingLoose,
			dstLen: 2, wantRead: 0, wantWritten: 0,
		},
		{
			name: "partial-write-before-error", input: " \naGVs \t#bad", handling: base64LastChunkHandlingLoose,
			dstLen: 8, wantRead: 6, wantWritten: 3, want: "hel", wantErr: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			dst := make([]byte, test.dstLen)
			read, written, err := fromBase64Into(test.input, &base64DecodeMap, test.handling, dst)
			if (err != nil) != test.wantErr {
				t.Fatalf("error = %v, wantErr = %v", err, test.wantErr)
			}
			if read != test.wantRead || written != test.wantWritten {
				t.Fatalf("read, written = %d, %d; want %d, %d", read, written, test.wantRead, test.wantWritten)
			}
			if got := string(dst[:written]); got != test.want {
				t.Fatalf("decoded = %q, want %q", got, test.want)
			}
		})
	}
}

func TestBase64AsciiFastPathMatchesUnicodeReference(t *testing.T) {
	// The Unicode decoder retains the original chunk-at-a-time implementation,
	// making it a useful independent oracle for ASCII code units.
	const chars = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/-_= \t\r\n\f#"
	seed := uint64(0x6a09e667f3bcc909)
	next := func(n int) int {
		seed = seed*6364136223846793005 + 1442695040888963407
		return int((seed >> 32) % uint64(n))
	}
	for iteration := 0; iteration < 5000; iteration++ {
		inputBytes := make([]byte, next(65))
		unicodeInput := make(unicodeString, len(inputBytes)+1)
		unicodeInput[0] = 0xFEFF
		for i := range inputBytes {
			inputBytes[i] = chars[next(len(chars))]
			unicodeInput[i+1] = uint16(inputBytes[i])
		}
		input := asciiString(inputBytes)
		decodeMap := &base64DecodeMap
		if next(2) != 0 {
			decodeMap = &base64DecodeMapUrl
		}
		handling := base64LastChunkHandling(next(3) + 1)
		dstLen := next(49)
		asciiDst, unicodeDst := make([]byte, dstLen), make([]byte, dstLen)
		asciiRead, asciiWritten, asciiErr := fromBase64Into(input, decodeMap, handling, asciiDst)
		unicodeRead, unicodeWritten, unicodeErr := fromBase64Into(unicodeInput, decodeMap, handling, unicodeDst)

		asciiErrText, unicodeErrText := "", ""
		if asciiErr != nil {
			asciiErrText = asciiErr.Error()
		}
		if unicodeErr != nil {
			unicodeErrText = unicodeErr.Error()
		}
		if asciiRead != unicodeRead || asciiWritten != unicodeWritten || asciiErrText != unicodeErrText ||
			!bytes.Equal(asciiDst, unicodeDst) {
			t.Fatalf("iteration %d input %q dstLen %d: ASCII=(%d,%d,%q,%x), Unicode=(%d,%d,%q,%x)",
				iteration, input, dstLen,
				asciiRead, asciiWritten, asciiErrText, asciiDst,
				unicodeRead, unicodeWritten, unicodeErrText, unicodeDst)
		}
	}
}

func TestUint8ArrayToBase64ResultOwnsBacking(t *testing.T) {
	r := New()
	data := bytes.Repeat([]byte{0xa7}, 64<<10)
	ta := r.newTypedArrayWithData(data, r.getUint8Array(), r.newUint8ArrayObject, nil)
	got := r.uint8ArrayProto_toBase64(FunctionCall{This: ta.val}).String()
	want := stdbase64.StdEncoding.EncodeToString(data)

	// Mutating the source view after encoding must not affect the returned string.
	for i := range data {
		data[i] = byte(i)
	}
	runtime.GC()
	for range 8 {
		_ = make([]byte, 1<<20)
	}
	runtime.GC()
	if got != want {
		t.Fatal("encoded string changed after source mutation and garbage collection")
	}
}

func TestUint8ArraySetFromResultShape(t *testing.T) {
	const script = `
		function check(result, expectedRead, expectedWritten) {
			assert.sameValue(Object.getPrototypeOf(result), Object.prototype, "prototype");
			assert.sameValue(Object.keys(result).join(","), "read,written", "property order");
			for (const name of ["read", "written"]) {
				const desc = Object.getOwnPropertyDescriptor(result, name);
				assert.sameValue(desc.writable, true, name + " writable");
				assert.sameValue(desc.enumerable, true, name + " enumerable");
				assert.sameValue(desc.configurable, true, name + " configurable");
			}
			assert.sameValue(result.read, expectedRead, "read");
			assert.sameValue(result.written, expectedWritten, "written");
		}
		check(new Uint8Array(3).setFromBase64("aGVs"), 4, 3);
		check(new Uint8Array(3).setFromHex("a7b8c9"), 6, 3);
	`
	testScriptWithTestLib(script, _undefined, t)
}

func BenchmarkBase64DecodeModes(b *testing.B) {
	const decodedLen = 64 << 10
	clean := benchmarkBase64EncodedInput(decodedLen, stdbase64.StdEncoding, 0)
	cleanURL := benchmarkBase64EncodedInput(decodedLen, stdbase64.URLEncoding, 0)
	pem := benchmarkBase64EncodedInput(decodedLen, stdbase64.StdEncoding, 76)
	heavy := benchmarkBase64EncodedInput(decodedLen, stdbase64.StdEncoding, 4)
	heavyURL := benchmarkBase64EncodedInput(decodedLen, stdbase64.URLEncoding, 4)
	partial := asciiString(strings.TrimRight(string(heavy), "= \r\n\t\f"))
	invalidFirst := asciiString("#" + string(clean[1:]))
	invalidLast := asciiString(string(clean[:len(clean)-1]) + "#")

	tests := []struct {
		name        string
		input       asciiString
		decodeMap   *[256]byte
		handling    base64LastChunkHandling
		dstLen      int
		wantErr     bool
		wantWritten int
	}{
		{name: "clean/base64/loose", input: clean, decodeMap: &base64DecodeMap, handling: base64LastChunkHandlingLoose, dstLen: decodedLen, wantWritten: decodedLen},
		{name: "clean/base64url/loose", input: cleanURL, decodeMap: &base64DecodeMapUrl, handling: base64LastChunkHandlingLoose, dstLen: decodedLen, wantWritten: decodedLen},
		{name: "PEM-whitespace/base64/loose", input: pem, decodeMap: &base64DecodeMap, handling: base64LastChunkHandlingLoose, dstLen: decodedLen, wantWritten: decodedLen},
		{name: "heavy-whitespace/base64/loose", input: heavy, decodeMap: &base64DecodeMap, handling: base64LastChunkHandlingLoose, dstLen: decodedLen, wantWritten: decodedLen},
		{name: "heavy-whitespace/base64url/loose", input: heavyURL, decodeMap: &base64DecodeMapUrl, handling: base64LastChunkHandlingLoose, dstLen: decodedLen, wantWritten: decodedLen},
		{name: "heavy-whitespace/base64/strict", input: heavy, decodeMap: &base64DecodeMap, handling: base64LastChunkHandlingStrict, dstLen: decodedLen, wantWritten: decodedLen},
		{name: "heavy-whitespace/base64/stop-before-partial", input: partial, decodeMap: &base64DecodeMap, handling: base64LastChunkHandlingStop, dstLen: decodedLen, wantWritten: decodedLen - decodedLen%3},
		{name: "clean/base64/short-destination", input: clean, decodeMap: &base64DecodeMap, handling: base64LastChunkHandlingLoose, dstLen: 16, wantWritten: 15},
		{name: "heavy-whitespace/base64/short-destination", input: heavy, decodeMap: &base64DecodeMap, handling: base64LastChunkHandlingLoose, dstLen: 16, wantWritten: 15},
		{name: "error/invalid-first", input: invalidFirst, decodeMap: &base64DecodeMap, handling: base64LastChunkHandlingLoose, dstLen: decodedLen, wantErr: true},
		{name: "error/invalid-last", input: invalidLast, decodeMap: &base64DecodeMap, handling: base64LastChunkHandlingLoose, dstLen: decodedLen, wantErr: true},
	}

	for _, test := range tests {
		b.Run(test.name, func(b *testing.B) {
			dst := make([]byte, test.dstLen)
			b.ReportAllocs()
			b.SetBytes(int64(test.wantWritten))
			var read, written int
			var err error
			for b.Loop() {
				read, written, err = fromBase64Into(test.input, test.decodeMap, test.handling, dst)
			}
			if (err != nil) != test.wantErr {
				b.Fatalf("error = %v, wantErr = %v", err, test.wantErr)
			}
			if !test.wantErr && written != test.wantWritten {
				b.Fatalf("written = %d, want %d", written, test.wantWritten)
			}
			base64BenchmarkRead, base64BenchmarkWritten = read, written
		})
	}
}

func BenchmarkUint8ArrayFromBase64Retention(b *testing.B) {
	const decodedLen = 1 << 20
	clean := benchmarkBase64EncodedInput(decodedLen, stdbase64.StdEncoding, 0)
	pem := benchmarkBase64EncodedInput(decodedLen, stdbase64.StdEncoding, 76)
	heavy := benchmarkBase64EncodedInput(decodedLen, stdbase64.StdEncoding, 4)
	tests := []struct {
		name    string
		input   asciiString
		wantErr bool
	}{
		{name: "clean", input: clean},
		{name: "PEM-whitespace", input: pem},
		{name: "heavy-whitespace", input: heavy},
		{name: "whitespace-only", input: asciiString(strings.Repeat(" \t\r\n\f", decodedLen/5))},
		{name: "invalid-first", input: asciiString("#" + strings.Repeat("A", len(clean)-1)), wantErr: true},
	}

	for _, test := range tests {
		b.Run(test.name, func(b *testing.B) {
			b.ReportAllocs()
			var decoded []byte
			var err error
			for b.Loop() {
				_, decoded, err = fromBase64(test.input, &base64DecodeMap, base64LastChunkHandlingLoose)
			}
			if (err != nil) != test.wantErr {
				b.Fatalf("error = %v, wantErr = %v", err, test.wantErr)
			}
			b.ReportMetric(float64(cap(decoded)), "backing-cap-B")
			base64BenchmarkBytes = decoded
		})
	}
}

func BenchmarkUint8ArraySetFromResult(b *testing.B) {
	r := New()
	tests := []struct {
		name  string
		input String
		f     func(FunctionCall) Value
	}{
		{name: "base64/empty", input: asciiString(""), f: r.uint8ArrayProto_setFromBase64},
		{name: "base64/three-bytes", input: asciiString("aGVs"), f: r.uint8ArrayProto_setFromBase64},
		{name: "hex/empty", input: asciiString(""), f: r.uint8ArrayProto_setFromHex},
		{name: "hex/three-bytes", input: asciiString("a7b8c9"), f: r.uint8ArrayProto_setFromHex},
	}
	for _, test := range tests {
		b.Run(test.name, func(b *testing.B) {
			call := FunctionCall{
				This:      r.newTypedArrayWithData(make([]byte, 3), r.getUint8Array(), r.newUint8ArrayObject, nil).val,
				Arguments: []Value{test.input},
			}
			b.ReportAllocs()
			var result Value
			for b.Loop() {
				result = test.f(call)
			}
			base64BenchmarkValue = result
		})
	}
}
