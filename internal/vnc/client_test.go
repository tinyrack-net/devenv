package vnc

import (
	"image/png"
	"net"
	"os"
	"testing"
	"time"
)

// TestRealServer runs against a TigerVNC server when DEVENV_VNC_TEST_ADDR is
// set (e.g. the desktop image with port 5901 published).
func TestRealServer(t *testing.T) {
	addr := os.Getenv("DEVENV_VNC_TEST_ADDR")
	if addr == "" {
		t.Skip("DEVENV_VNC_TEST_ADDR not set")
	}
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	c, err := Connect(conn)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	t.Logf("desktop %q %dx%d", c.Name, c.Width, c.Height)

	start := time.Now()
	img, err := c.Capture(10 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("capture %v", time.Since(start))
	// A real desktop is not a single colour.
	first := img.RGBAAt(0, 0)
	varied := false
	for y := 0; y < img.Rect.Dy() && !varied; y += 37 {
		for x := 0; x < img.Rect.Dx(); x += 41 {
			if img.RGBAAt(x, y) != first {
				varied = true
				break
			}
		}
	}
	if !varied {
		t.Error("captured frame is a single colour")
	}
	if out := os.Getenv("DEVENV_VNC_TEST_OUT"); out != "" {
		f, _ := os.Create(out)
		png.Encode(f, img)
		f.Close()
	}
	if err := c.Click(c.Width/2, c.Height/2, ButtonLeft, 1); err != nil {
		t.Fatal(err)
	}
	if err := c.Keys("ctrl+alt+t"); err != nil {
		t.Fatal(err)
	}
	if err := c.Keys("hyper+x"); err == nil {
		t.Error("unknown key accepted")
	}
	// The connection must still serve captures after input.
	if _, err := c.Capture(10 * time.Second); err != nil {
		t.Fatal(err)
	}
}

func TestKeysyms(t *testing.T) {
	for r, want := range map[rune]uint32{'a': 'a', 'Z': 'Z', 'é': 0xe9, '한': 0x0100d55c, '\n': 0xff0d} {
		if got, ok := runeKeysym(r); !ok || got != want {
			t.Errorf("%q = %#x %v, want %#x", r, got, ok, want)
		}
	}
	if _, ok := runeKeysym(0x01); ok {
		t.Error("control char should be skipped")
	}
}
