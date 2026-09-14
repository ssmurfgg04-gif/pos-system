package escpos

import (
        "bytes"
        "testing"
)

func TestCode39FramesAlphanumericPayload(t *testing.T) {
        var buf bytes.Buffer
        e := New(&buf)
        if _, err := e.CODE39("abc-123"); err != nil {
                t.Fatal(err)
        }
        e.dst.Flush()
        got := buf.Bytes()
        // GS k 4 "ABC-123" NUL — lowercased input uppercased on the wire.
        want := append([]byte{0x1D, 'k', 4}, append([]byte("ABC-123"), 0)...)
        if !bytes.Equal(got, want) {
                t.Fatalf("CODE39 bytes: %q want %q", got, want)
        }
        if _, err := e.CODE39(""); err == nil {
                t.Fatal("empty CODE39 must fail")
        }
        if _, err := e.CODE39("caf\u00e9"); err == nil {
                t.Fatal("non-CODE39 charset must fail")
        }
}

func TestITFRequiresEvenDigits(t *testing.T) {
        var buf bytes.Buffer
        e := New(&buf)
        if _, err := e.ITF("1234"); err != nil {
                t.Fatal(err)
        }
        e.dst.Flush()
        want := append([]byte{0x1D, 'k', 5}, append([]byte("1234"), 0)...)
        if !bytes.Equal(buf.Bytes(), want) {
                t.Fatalf("ITF bytes: %q", buf.Bytes())
        }
        if _, err := e.ITF("123"); err == nil {
                t.Fatal("odd-length ITF must fail")
        }
        if _, err := e.ITF("12a4"); err == nil {
                t.Fatal("non-digit ITF must fail")
        }
}

func TestCodabarFramesStartStopAndSymbols(t *testing.T) {
        var buf bytes.Buffer
        e := New(&buf)
        if _, err := e.CODABAR("A1234B"); err != nil {
                t.Fatal(err)
        }
        e.dst.Flush()
        want := append([]byte{0x1D, 'k', 6}, append([]byte("A1234B"), 0)...)
        if !bytes.Equal(buf.Bytes(), want) {
                t.Fatalf("CODABAR bytes: %q", buf.Bytes())
        }
        if _, err := e.CODABAR(""); err == nil {
                t.Fatal("empty CODABAR must fail")
        }
        if _, err := e.CODABAR("AE"); err == nil {
                t.Fatal("E is not a CODABAR character")
        }
}
