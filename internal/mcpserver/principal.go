package mcpserver

import (
	"context"
	"fmt"
	"reflect"
	"strings"

	"github.com/tinyrack-net/devenv/internal/model"
	"github.com/tinyrack-net/devenv/internal/access"
	"github.com/tinyrack-net/devenv/internal/activity"
	"github.com/tinyrack-net/devenv/internal/auth"
)

type activityKey struct{}

// who returns the caller. Requests always pass authenticate first; the empty
// principal (no access to anything) is a safe fallback.
func who(ctx context.Context) *auth.Principal {
	if p := auth.From(ctx); p != nil {
		return p
	}
	return &auth.Principal{}
}

func activityLog(ctx context.Context) *activity.Log {
	l, _ := ctx.Value(activityKey{}).(*activity.Log)
	return l
}

func sharedWith(p *auth.Principal, e *model.Environment) bool {
	personal := &auth.Principal{Name: p.Name, Groups: p.Groups}
	return access.Environment(personal, e) != access.None
}

// describe extracts the environment name and a one line summary from a tool
// input struct: the "name" field, and the most telling other field.
func describe(in any) (env, summary string) {
	v := reflect.ValueOf(in)
	if v.Kind() == reflect.Pointer {
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return "", ""
	}
	t := v.Type()
	field := func(json string) (string, bool) {
		for i := 0; i < t.NumField(); i++ {
			tag := strings.Split(t.Field(i).Tag.Get("json"), ",")[0]
			if tag != json {
				continue
			}
			f := v.Field(i)
			if f.Kind() == reflect.Pointer {
				if f.IsNil() {
					return "", false
				}
				f = f.Elem()
			}
			switch f.Kind() {
			case reflect.String:
				return f.String(), f.String() != ""
			case reflect.Int, reflect.Int32, reflect.Int64:
				return fmt.Sprint(f.Int()), true
			}
		}
		return "", false
	}
	env, _ = field("name")
	for _, k := range []string{"command", "path", "pattern", "text", "keys", "id", "image"} {
		if s, ok := field(k); ok {
			if k == "id" {
				s = "process " + s
			}
			summary = s
			break
		}
	}
	if x, ok := field("x"); ok && summary == "" {
		y, _ := field("y")
		summary = "at " + x + "," + y
	}
	if len(summary) > 300 {
		summary = summary[:300] + "…"
	}
	return env, summary
}

// detail renders a short result text for the activity log: command output,
// diffs and errors are what a human wants to see.
func detail(out any) string {
	v := reflect.ValueOf(out)
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return ""
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return ""
	}
	t := v.Type()
	var parts []string
	for i := 0; i < t.NumField(); i++ {
		tag := strings.Split(t.Field(i).Tag.Get("json"), ",")[0]
		f := v.Field(i)
		switch tag {
		case "exit_code":
			parts = append(parts, fmt.Sprintf("exit %d", f.Int()))
		case "stdout", "stderr", "output", "diff":
			if f.Kind() == reflect.String && f.String() != "" {
				s := f.String()
				if len(s) > 4000 {
					s = "…" + s[len(s)-4000:]
				}
				parts = append(parts, s)
			}
		}
	}
	return strings.Join(parts, "\n")
}
