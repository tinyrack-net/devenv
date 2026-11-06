// Package vnc is a small RFB (VNC) client used to give agents eyes and hands
// on desktop environments: framebuffer capture and pointer/keyboard events.
//
// It speaks RFB 3.8 with security type None (the environment's VNC server
// listens on localhost only and is reached through the authenticated SSH
// tunnel) and the Raw encoding, which every server supports. Raw is
// bandwidth heavy but the tunnel is local to the cluster and captures are
// on demand.
package vnc

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"io"
	"net"
	"time"
)

// Client is one RFB connection.
type Client struct {
	conn   net.Conn
	r      *bufio.Reader
	Width  int
	Height int
	Name   string
}

// Connect performs the RFB handshake over conn.
func Connect(conn net.Conn) (*Client, error) {
	c := &Client{conn: conn, r: bufio.NewReaderSize(conn, 1<<16)}
	_ = conn.SetDeadline(time.Now().Add(15 * time.Second))
	defer conn.SetDeadline(time.Time{})

	var ver [12]byte
	if _, err := io.ReadFull(c.r, ver[:]); err != nil {
		return nil, fmt.Errorf("read version: %w", err)
	}
	if string(ver[:4]) != "RFB " {
		return nil, fmt.Errorf("not a VNC server (%q)", ver)
	}
	if _, err := conn.Write([]byte("RFB 003.008\n")); err != nil {
		return nil, err
	}
	// Security types.
	n, err := c.r.ReadByte()
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, c.reason()
	}
	types := make([]byte, n)
	if _, err := io.ReadFull(c.r, types); err != nil {
		return nil, err
	}
	hasNone := false
	for _, t := range types {
		if t == 1 {
			hasNone = true
		}
	}
	if !hasNone {
		return nil, fmt.Errorf("VNC server requires authentication (security types %v); devenv expects no password on 127.0.0.1:5901", types)
	}
	if _, err := conn.Write([]byte{1}); err != nil {
		return nil, err
	}
	var result uint32
	if err := binary.Read(c.r, binary.BigEndian, &result); err != nil {
		return nil, err
	}
	if result != 0 {
		return nil, c.reason()
	}
	// ClientInit: shared session, so the person's viewer stays connected.
	if _, err := conn.Write([]byte{1}); err != nil {
		return nil, err
	}
	// ServerInit.
	var init struct {
		W, H uint16
		PF   [16]byte
		NLen uint32
	}
	if err := binary.Read(c.r, binary.BigEndian, &init); err != nil {
		return nil, fmt.Errorf("server init: %w", err)
	}
	name := make([]byte, init.NLen)
	if _, err := io.ReadFull(c.r, name); err != nil {
		return nil, err
	}
	c.Width, c.Height, c.Name = int(init.W), int(init.H), string(name)

	// Ask for 32bpp little endian RGBX so decoding is a copy.
	pf := []byte{0, 0, 0, 0, 32, 24, 0, 1, 0, 255, 0, 255, 0, 255, 0, 8, 16, 0, 0, 0}
	if _, err := conn.Write(pf); err != nil {
		return nil, err
	}
	// SetEncodings: Raw, DesktopSize (to learn about resizes).
	enc := []byte{2, 0, 0, 2, 0, 0, 0, 0, 0xff, 0xff, 0xff, 0x21}
	if _, err := conn.Write(enc); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Client) reason() error {
	var l uint32
	if err := binary.Read(c.r, binary.BigEndian, &l); err != nil {
		return errors.New("VNC handshake refused")
	}
	b := make([]byte, min(l, 4096))
	_, _ = io.ReadFull(c.r, b)
	return fmt.Errorf("VNC handshake refused: %s", b)
}

// Close closes the connection.
func (c *Client) Close() error { return c.conn.Close() }

// Capture requests a full framebuffer update and returns it.
func (c *Client) Capture(timeout time.Duration) (*image.RGBA, error) {
	_ = c.conn.SetDeadline(time.Now().Add(timeout))
	defer c.conn.SetDeadline(time.Time{})
	req := make([]byte, 10)
	req[0] = 3 // FramebufferUpdateRequest
	req[1] = 0 // non-incremental
	binary.BigEndian.PutUint16(req[6:], uint16(c.Width))
	binary.BigEndian.PutUint16(req[8:], uint16(c.Height))
	if _, err := c.conn.Write(req); err != nil {
		return nil, err
	}
	img := image.NewRGBA(image.Rect(0, 0, c.Width, c.Height))
	for {
		msg, err := c.r.ReadByte()
		if err != nil {
			return nil, err
		}
		switch msg {
		case 0: // FramebufferUpdate
			done, err := c.readUpdate(&img)
			if err != nil {
				return nil, err
			}
			if done {
				return img, nil
			}
			// Size changed: request the new full frame.
			binary.BigEndian.PutUint16(req[6:], uint16(c.Width))
			binary.BigEndian.PutUint16(req[8:], uint16(c.Height))
			if _, err := c.conn.Write(req); err != nil {
				return nil, err
			}
		case 1: // SetColourMapEntries (not used with true colour)
			var hdr struct {
				Pad   uint8
				First uint16
				N     uint16
			}
			if err := binary.Read(c.r, binary.BigEndian, &hdr); err != nil {
				return nil, err
			}
			if _, err := io.CopyN(io.Discard, c.r, int64(hdr.N)*6); err != nil {
				return nil, err
			}
		case 2: // Bell
		case 3: // ServerCutText
			var hdr struct {
				Pad [3]byte
				Len uint32
			}
			if err := binary.Read(c.r, binary.BigEndian, &hdr); err != nil {
				return nil, err
			}
			if _, err := io.CopyN(io.Discard, c.r, int64(hdr.Len)); err != nil {
				return nil, err
			}
		default:
			return nil, fmt.Errorf("unsupported VNC message %d", msg)
		}
	}
}

// readUpdate decodes one FramebufferUpdate into img. It returns false when
// the desktop was resized (img is reallocated and a new frame is needed).
func (c *Client) readUpdate(img **image.RGBA) (bool, error) {
	var hdr struct {
		Pad uint8
		N   uint16
	}
	if err := binary.Read(c.r, binary.BigEndian, &hdr); err != nil {
		return false, err
	}
	complete := true
	for i := 0; i < int(hdr.N); i++ {
		var rect struct {
			X, Y, W, H uint16
			Enc        int32
		}
		if err := binary.Read(c.r, binary.BigEndian, &rect); err != nil {
			return false, err
		}
		switch rect.Enc {
		case 0: // Raw, 4 bytes per pixel RGBX
			row := make([]byte, int(rect.W)*4)
			for y := 0; y < int(rect.H); y++ {
				if _, err := io.ReadFull(c.r, row); err != nil {
					return false, err
				}
				py := int(rect.Y) + y
				if py >= (*img).Rect.Dy() {
					continue
				}
				off := (*img).PixOffset(int(rect.X), py)
				n := min(len(row), len((*img).Pix)-off, ((*img).Rect.Dx()-int(rect.X))*4)
				for k := 0; k+3 < n; k += 4 {
					(*img).Pix[off+k] = row[k]
					(*img).Pix[off+k+1] = row[k+1]
					(*img).Pix[off+k+2] = row[k+2]
					(*img).Pix[off+k+3] = 255
				}
			}
		case -223: // DesktopSize
			c.Width, c.Height = int(rect.W), int(rect.H)
			*img = image.NewRGBA(image.Rect(0, 0, c.Width, c.Height))
			complete = false
		default:
			return false, fmt.Errorf("unsupported VNC encoding %d", rect.Enc)
		}
	}
	return complete, nil
}

// Pointer moves the pointer to (x, y) with the given button mask
// (bit 0 left, 1 middle, 2 right, 3 wheel up, 4 wheel down).
func (c *Client) Pointer(x, y int, mask uint8) error {
	b := []byte{5, mask, 0, 0, 0, 0}
	binary.BigEndian.PutUint16(b[2:], uint16(clamp(x, 0, c.Width-1)))
	binary.BigEndian.PutUint16(b[4:], uint16(clamp(y, 0, c.Height-1)))
	_, err := c.conn.Write(b)
	return err
}

// Key sends a key press or release of an X keysym.
func (c *Client) Key(keysym uint32, down bool) error {
	b := []byte{4, 0, 0, 0, 0, 0, 0, 0}
	if down {
		b[1] = 1
	}
	binary.BigEndian.PutUint32(b[4:], keysym)
	_, err := c.conn.Write(b)
	return err
}

func clamp(v, lo, hi int) int { return max(lo, min(v, hi)) }
