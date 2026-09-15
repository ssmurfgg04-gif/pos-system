package escpos

import (
        "bytes"
        "testing"
)

func TestKickDrawerBytes(t *testing.T) {
        var buf bytes.Buffer
        e := New(&buf)
        if _, err := e.KickDrawer(0, 25, 250); err != nil {
                t.Fatal(err)
        }
        if err := e.Print(); err != nil {
                t.Fatal(err)
        }
        want := []byte{0x1B, 'p', 0, 25, 250}
        if !bytes.Equal(buf.Bytes(), want) {
                t.Fatalf("kick bytes: %q want %q", buf.Bytes(), want)
        }
}

func TestKickDrawerClampsPin(t *testing.T) {
        var buf bytes.Buffer
        e := New(&buf)
        if _, err := e.KickDrawer(9, 25, 250); err != nil {
                t.Fatal(err)
        }
        if err := e.Print(); err != nil {
                t.Fatal(err)
        }
        if buf.Bytes()[2] != 0 {
                t.Fatalf("pin should clamp to 0, got %d", buf.Bytes()[2])
        }
}
