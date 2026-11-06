package vnc

import (
	"fmt"
	"strings"
	"time"
	"unicode"
)

// Buttons for Click.
const (
	ButtonLeft   uint8 = 1 << 0
	ButtonMiddle uint8 = 1 << 1
	ButtonRight  uint8 = 1 << 2
	wheelUp      uint8 = 1 << 3
	wheelDown    uint8 = 1 << 4
	wheelLeft    uint8 = 1 << 5
	wheelRight   uint8 = 1 << 6
)

// pause between synthetic events so applications see distinct events.
const pause = 15 * time.Millisecond

// typePause between typed characters. Applications that just got focus
// (a window that is still mapping) drop events sent faster than this.
const typePause = 8 * time.Millisecond

// Click moves to (x, y) and clicks count times.
func (c *Client) Click(x, y int, button uint8, count int) error {
	if err := c.Pointer(x, y, 0); err != nil {
		return err
	}
	for i := 0; i < max(count, 1); i++ {
		time.Sleep(pause)
		if err := c.Pointer(x, y, button); err != nil {
			return err
		}
		time.Sleep(pause)
		if err := c.Pointer(x, y, 0); err != nil {
			return err
		}
	}
	return nil
}

// Drag presses at (x1, y1), moves to (x2, y2) and releases.
func (c *Client) Drag(x1, y1, x2, y2 int) error {
	steps := 10
	if err := c.Pointer(x1, y1, 0); err != nil {
		return err
	}
	time.Sleep(pause)
	if err := c.Pointer(x1, y1, ButtonLeft); err != nil {
		return err
	}
	for i := 1; i <= steps; i++ {
		time.Sleep(pause)
		x := x1 + (x2-x1)*i/steps
		y := y1 + (y2-y1)*i/steps
		if err := c.Pointer(x, y, ButtonLeft); err != nil {
			return err
		}
	}
	time.Sleep(pause)
	return c.Pointer(x2, y2, 0)
}

// Scroll sends wheel clicks at (x, y): dy > 0 scrolls down, dx > 0 right.
func (c *Client) Scroll(x, y, dx, dy int) error {
	wheel := func(b uint8, n int) error {
		for i := 0; i < n; i++ {
			if err := c.Pointer(x, y, b); err != nil {
				return err
			}
			time.Sleep(pause)
			if err := c.Pointer(x, y, 0); err != nil {
				return err
			}
			time.Sleep(pause)
		}
		return nil
	}
	switch {
	case dy > 0:
		if err := wheel(wheelDown, dy); err != nil {
			return err
		}
	case dy < 0:
		if err := wheel(wheelUp, -dy); err != nil {
			return err
		}
	}
	switch {
	case dx > 0:
		return wheel(wheelRight, dx)
	case dx < 0:
		return wheel(wheelLeft, -dx)
	}
	return nil
}

// Type types text. Characters are sent as keysyms (Latin-1 directly,
// others as Unicode keysyms 0x01000000+codepoint, which X servers map).
func (c *Client) Type(text string) error {
	for _, r := range text {
		ks, ok := runeKeysym(r)
		if !ok {
			continue
		}
		if err := c.tap(ks); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) tap(ks uint32) error {
	if err := c.Key(ks, true); err != nil {
		return err
	}
	time.Sleep(typePause)
	err := c.Key(ks, false)
	time.Sleep(typePause)
	return err
}

func runeKeysym(r rune) (uint32, bool) {
	switch r {
	case '\n', '\r':
		return keysyms["enter"], true
	case '\t':
		return keysyms["tab"], true
	case '\b':
		return keysyms["backspace"], true
	}
	if r < 0x20 || r == 0x7f {
		return 0, false
	}
	if r <= 0xff {
		return uint32(r), true
	}
	if unicode.IsPrint(r) {
		return 0x01000000 + uint32(r), true
	}
	return 0, false
}

// Keys presses a combination like "ctrl+shift+t", "enter" or "alt+F4":
// modifiers are held while the last key is tapped.
func (c *Client) Keys(combo string) error {
	parts := strings.Split(combo, "+")
	var syms []uint32
	for i, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" && i == len(parts)-1 {
			p = "+" // "ctrl++"
		}
		ks, ok := keysyms[strings.ToLower(p)]
		if !ok {
			runes := []rune(p)
			if len(runes) != 1 {
				return fmt.Errorf("unknown key %q (use names like ctrl, shift, alt, super, enter, tab, escape, backspace, delete, up, down, left, right, home, end, pageup, pagedown, f1-f12 or a single character)", p)
			}
			r := runes[0]
			if len(syms) > 0 && unicode.IsUpper(r) {
				r = unicode.ToLower(r) // with modifiers, letters are lower case keys
			}
			ks, ok = runeKeysym(r)
			if !ok {
				return fmt.Errorf("unsupported key %q", p)
			}
		}
		syms = append(syms, ks)
	}
	for _, ks := range syms {
		if err := c.Key(ks, true); err != nil {
			return err
		}
		time.Sleep(pause / 3)
	}
	for i := len(syms) - 1; i >= 0; i-- {
		if err := c.Key(syms[i], false); err != nil {
			return err
		}
		time.Sleep(pause / 3)
	}
	return nil
}

var keysyms = map[string]uint32{
	"enter": 0xff0d, "return": 0xff0d, "tab": 0xff09, "escape": 0xff1b, "esc": 0xff1b,
	"backspace": 0xff08, "delete": 0xffff, "del": 0xffff, "insert": 0xff63, "space": 0x20,
	"home": 0xff50, "end": 0xff57, "pageup": 0xff55, "pagedown": 0xff56,
	"left": 0xff51, "up": 0xff52, "right": 0xff53, "down": 0xff54,
	"shift": 0xffe1, "ctrl": 0xffe3, "control": 0xffe3, "alt": 0xffe9, "meta": 0xffe7,
	"super": 0xffeb, "win": 0xffeb, "cmd": 0xffeb,
	"f1": 0xffbe, "f2": 0xffbf, "f3": 0xffc0, "f4": 0xffc1, "f5": 0xffc2, "f6": 0xffc3,
	"f7": 0xffc4, "f8": 0xffc5, "f9": 0xffc6, "f10": 0xffc7, "f11": 0xffc8, "f12": 0xffc9,
	"printscreen": 0xff61, "menu": 0xff67,
}
