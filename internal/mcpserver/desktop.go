package mcpserver

import (
	"bytes"
	"context"
	"fmt"
	"image/png"
	"net"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	devenvv1 "github.com/tinyrack-net/devenv/api/v1alpha1"
	"github.com/tinyrack-net/devenv/internal/controller"
	"github.com/tinyrack-net/devenv/internal/vnc"
)

// Desktop tools speak VNC to the environment over the SSH tunnel; nothing
// beyond the VNC server on 127.0.0.1:5901 is needed in the image. A VNC
// session is kept per environment (shared, so people watching in the web
// console stay connected) and closed when idle.

type desktopSession struct {
	mu       sync.Mutex
	client   *vnc.Client
	release  func()
	lastUsed time.Time
}

type desktops struct {
	mu       sync.Mutex
	sessions map[string]*desktopSession
	once     sync.Once
}

func (d *desktops) reaper() {
	for range time.Tick(30 * time.Second) {
		d.mu.Lock()
		for name, s := range d.sessions {
			if s.mu.TryLock() {
				if time.Since(s.lastUsed) > 2*time.Minute {
					s.close()
					delete(d.sessions, name)
				}
				s.mu.Unlock()
			}
		}
		d.mu.Unlock()
	}
}

func (s *desktopSession) close() {
	if s.client != nil {
		s.client.Close()
		s.client = nil
	}
	if s.release != nil {
		s.release()
		s.release = nil
	}
}

// withDesktop runs fn with a connected VNC client for the environment.
func (s *Server) withDesktop(ctx context.Context, name string, fn func(*vnc.Client) error) error {
	env, err := s.getEnv(ctx, name)
	if err != nil {
		return userErr(err)
	}
	if !env.Status.Desktop {
		return errorf("environment %s has no desktop (image %s); create one with an image that has desktop true in list_images", name, env.Spec.ImageRef)
	}
	if env.Status.Phase != devenvv1.PhaseRunning {
		if _, err := s.API.SetRunning(ctx, name, true); err != nil {
			return userErr(err)
		}
		if _, err := s.waitFor(ctx, name, true); err != nil {
			return err
		}
	}
	s.desk.once.Do(func() {
		s.desk.sessions = map[string]*desktopSession{}
		go s.desk.reaper()
	})
	s.desk.mu.Lock()
	ds := s.desk.sessions[name]
	if ds == nil {
		ds = &desktopSession{}
		s.desk.sessions[name] = ds
	}
	s.desk.mu.Unlock()

	ds.mu.Lock()
	defer ds.mu.Unlock()
	ds.lastUsed = time.Now()
	for attempt := 0; attempt < 2; attempt++ {
		if ds.client == nil {
			if err := s.dialDesktop(ctx, name, ds); err != nil {
				return err
			}
		}
		err := fn(ds.client)
		if err == nil {
			return nil
		}
		// A dropped tunnel (environment restarted): reconnect once.
		if _, ok := err.(*toolError); ok {
			return err
		}
		ds.close()
		if attempt == 1 {
			return errorf("desktop: %v", err)
		}
	}
	return nil
}

func (s *Server) dialDesktop(ctx context.Context, name string, ds *desktopSession) error {
	lease, err := s.leaseWithRetry(ctx, name)
	if err != nil {
		return errorf("could not connect to environment %s: %v", name, err)
	}
	var conn net.Conn
	addr := fmt.Sprintf("127.0.0.1:%d", controller.VNCPort)
	for i := 0; i < 20; i++ { // the desktop may still be starting
		conn, err = lease.Client.Dial("tcp", addr)
		if err == nil || ctx.Err() != nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if err != nil {
		lease.Release()
		return errorf("the desktop (VNC on %s) is not reachable in %s: %v", addr, name, err)
	}
	c, err := vnc.Connect(conn)
	if err != nil {
		conn.Close()
		lease.Release()
		return errorf("desktop: %v", err)
	}
	ds.client, ds.release = c, lease.Release
	return nil
}

// screenshotResult captures the screen, scaled for the model, and reports
// the scale so coordinates can be given in screenshot pixels.
func (s *Server) screenshot(c *vnc.Client, note string) (*mcp.CallToolResult, desktopOut, error) {
	img, err := c.Capture(15 * time.Second)
	if err != nil {
		return nil, desktopOut{}, err
	}
	shot := scaleDown(img, maxImageSide)
	b := shot.Bounds()
	var buf bytes.Buffer
	if err := (&png.Encoder{CompressionLevel: png.BestSpeed}).Encode(&buf, shot); err != nil {
		return nil, desktopOut{}, err
	}
	data, mime := buf.Bytes(), "image/png"
	if len(data) > maxImageBytes {
		data, mime, _, _, err = encodeForModel(shot, true)
		if err != nil {
			return nil, desktopOut{}, err
		}
	}
	out := desktopOut{ScreenWidth: c.Width, ScreenHeight: c.Height, ImageWidth: b.Dx(), ImageHeight: b.Dy(), Note: note}
	text := fmt.Sprintf("Screen %dx%d, screenshot %dx%d. Give coordinates in screenshot pixels.", c.Width, c.Height, b.Dx(), b.Dy())
	if note != "" {
		text = note + " " + text
	}
	return &mcp.CallToolResult{Content: []mcp.Content{
		&mcp.ImageContent{Data: data, MIMEType: mime},
		&mcp.TextContent{Text: text},
	}}, out, nil
}

// toScreen maps screenshot coordinates to screen coordinates.
func toScreen(c *vnc.Client, x, y int) (int, int) {
	w, h := c.Width, c.Height
	sw, sh := w, h
	if w > maxImageSide || h > maxImageSide {
		if w >= h {
			sw, sh = maxImageSide, max(1, h*maxImageSide/w)
		} else {
			sw, sh = max(1, w*maxImageSide/h), maxImageSide
		}
	}
	return x * w / sw, y * h / sh
}

type desktopOut struct {
	ScreenWidth  int    `json:"screen_width"`
	ScreenHeight int    `json:"screen_height"`
	ImageWidth   int    `json:"image_width" jsonschema:"screenshot size; coordinates are in this space"`
	ImageHeight  int    `json:"image_height"`
	Note         string `json:"note,omitempty"`
}

type screenshotIn struct {
	Name string `json:"name" jsonschema:"environment name (a desktop image)"`
}

type clickIn struct {
	Name   string `json:"name" jsonschema:"environment name"`
	X      int    `json:"x" jsonschema:"x in screenshot pixels"`
	Y      int    `json:"y" jsonschema:"y in screenshot pixels"`
	Button string `json:"button,omitempty" jsonschema:"left (default), right or middle"`
	Clicks int    `json:"clicks,omitempty" jsonschema:"1 (default) or 2 for double click"`
	// ToX/ToY make it a drag.
	ToX        *int  `json:"to_x,omitempty" jsonschema:"drag to this x (left button) instead of clicking"`
	ToY        *int  `json:"to_y,omitempty" jsonschema:"drag to this y"`
	Screenshot *bool `json:"screenshot,omitempty" jsonschema:"return a screenshot after the action (default true)"`
}

type scrollIn struct {
	Name       string `json:"name" jsonschema:"environment name"`
	X          int    `json:"x" jsonschema:"x in screenshot pixels"`
	Y          int    `json:"y" jsonschema:"y in screenshot pixels"`
	DeltaX     int    `json:"delta_x,omitempty" jsonschema:"wheel clicks right (negative: left)"`
	DeltaY     int    `json:"delta_y,omitempty" jsonschema:"wheel clicks down (negative: up), e.g. 5"`
	Screenshot *bool  `json:"screenshot,omitempty" jsonschema:"return a screenshot after the action (default true)"`
}

type typeIn struct {
	Name       string `json:"name" jsonschema:"environment name"`
	Text       string `json:"text" jsonschema:"text to type into the focused window; \\n presses Enter"`
	Screenshot *bool  `json:"screenshot,omitempty" jsonschema:"return a screenshot after the action (default true)"`
}

type keyIn struct {
	Name       string `json:"name" jsonschema:"environment name"`
	Keys       string `json:"keys" jsonschema:"key or combination, e.g. enter, ctrl+c, ctrl+shift+t, alt+f4, super"`
	Screenshot *bool  `json:"screenshot,omitempty" jsonschema:"return a screenshot after the action (default true)"`
}

// afterAction waits for the screen to settle and optionally captures it.
func (s *Server) afterAction(c *vnc.Client, want *bool, note string) (*mcp.CallToolResult, desktopOut, error) {
	if !boolDefault(want, true) {
		return nil, desktopOut{ScreenWidth: c.Width, ScreenHeight: c.Height, Note: note}, nil
	}
	time.Sleep(400 * time.Millisecond)
	return s.screenshot(c, note)
}

func button(name string) (uint8, error) {
	switch name {
	case "", "left":
		return vnc.ButtonLeft, nil
	case "right":
		return vnc.ButtonRight, nil
	case "middle":
		return vnc.ButtonMiddle, nil
	}
	return 0, errorf("button must be left, right or middle")
}

func (s *Server) registerDesktop(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{
		Name: "desktop_screenshot",
		Description: "Take a screenshot of an environment's desktop (desktop images only). " +
			"Use the returned image to find what to click; coordinates for the other desktop tools are in screenshot pixels.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
	}, logged("desktop_screenshot", func(ctx context.Context, _ *mcp.CallToolRequest, in screenshotIn) (*mcp.CallToolResult, desktopOut, error) {
		var res *mcp.CallToolResult
		var out desktopOut
		err := s.withDesktop(ctx, in.Name, func(c *vnc.Client) (err error) {
			res, out, err = s.screenshot(c, "")
			return err
		})
		return res, out, err
	}))

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "desktop_click",
		Description: "Click (or double click, right click, drag) on the desktop at screenshot coordinates. Returns a screenshot afterwards.",
	}, logged("desktop_click", func(ctx context.Context, _ *mcp.CallToolRequest, in clickIn) (*mcp.CallToolResult, desktopOut, error) {
		btn, err := button(in.Button)
		if err != nil {
			return nil, desktopOut{}, err
		}
		if (in.ToX == nil) != (in.ToY == nil) {
			return nil, desktopOut{}, errorf("set both to_x and to_y to drag")
		}
		var res *mcp.CallToolResult
		var out desktopOut
		err = s.withDesktop(ctx, in.Name, func(c *vnc.Client) error {
			x, y := toScreen(c, in.X, in.Y)
			if in.ToX != nil {
				tx, ty := toScreen(c, *in.ToX, *in.ToY)
				if err := c.Drag(x, y, tx, ty); err != nil {
					return err
				}
			} else if err := c.Click(x, y, btn, min(max(in.Clicks, 1), 3)); err != nil {
				return err
			}
			res, out, err = s.afterAction(c, in.Screenshot, "")
			return err
		})
		return res, out, err
	}))

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "desktop_scroll",
		Description: "Scroll with the mouse wheel at screenshot coordinates. Returns a screenshot afterwards.",
	}, logged("desktop_scroll", func(ctx context.Context, _ *mcp.CallToolRequest, in scrollIn) (*mcp.CallToolResult, desktopOut, error) {
		if in.DeltaX == 0 && in.DeltaY == 0 {
			return nil, desktopOut{}, errorf("set delta_y (down/up) or delta_x (right/left)")
		}
		var res *mcp.CallToolResult
		var out desktopOut
		err := s.withDesktop(ctx, in.Name, func(c *vnc.Client) error {
			x, y := toScreen(c, in.X, in.Y)
			if err := c.Scroll(x, y, max(-50, min(in.DeltaX, 50)), max(-50, min(in.DeltaY, 50))); err != nil {
				return err
			}
			var err error
			res, out, err = s.afterAction(c, in.Screenshot, "")
			return err
		})
		return res, out, err
	}))

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "desktop_type",
		Description: "Type text into the focused window of the desktop (Unicode supported; \\n presses Enter). Returns a screenshot afterwards. " +
			"After opening a window, take a screenshot first to make sure it is focused and ready.",
	}, logged("desktop_type", func(ctx context.Context, _ *mcp.CallToolRequest, in typeIn) (*mcp.CallToolResult, desktopOut, error) {
		if in.Text == "" {
			return nil, desktopOut{}, errorf("text is required")
		}
		if len(in.Text) > 10000 {
			return nil, desktopOut{}, errorf("text is longer than 10000 bytes; write files with write_file instead")
		}
		var res *mcp.CallToolResult
		var out desktopOut
		err := s.withDesktop(ctx, in.Name, func(c *vnc.Client) error {
			if err := c.Type(in.Text); err != nil {
				return err
			}
			var err error
			res, out, err = s.afterAction(c, in.Screenshot, "")
			return err
		})
		return res, out, err
	}))

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "desktop_key",
		Description: "Press a key or key combination on the desktop, e.g. enter, escape, ctrl+c, ctrl+shift+t, alt+f4. Returns a screenshot afterwards.",
	}, logged("desktop_key", func(ctx context.Context, _ *mcp.CallToolRequest, in keyIn) (*mcp.CallToolResult, desktopOut, error) {
		var res *mcp.CallToolResult
		var out desktopOut
		err := s.withDesktop(ctx, in.Name, func(c *vnc.Client) error {
			if err := c.Keys(in.Keys); err != nil {
				return &toolError{err.Error()}
			}
			var err error
			res, out, err = s.afterAction(c, in.Screenshot, "")
			return err
		})
		return res, out, err
	}))
}
