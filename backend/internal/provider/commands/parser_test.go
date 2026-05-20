package commands_test

import (
	"errors"
	"testing"

	"moses-chat-bot/backend/internal/provider/commands"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantErr error
		verb    string
		args    []string
	}{
		{name: "empty", in: "", wantErr: commands.ErrNotACommand},
		{name: "whitespace only", in: "   \t", wantErr: commands.ErrNotACommand},
		{name: "no slash", in: "hello", wantErr: commands.ErrNotACommand},
		{name: "plain text with words", in: "tell me about tickets", wantErr: commands.ErrNotACommand},

		{name: "start", in: "/start", verb: "/start"},
		{name: "start trailing ws", in: "/start    ", verb: "/start"},
		{name: "start mixed case", in: "/Start", verb: "/start"},
		{name: "start with extra arg", in: "/start now", wantErr: commands.ErrInvalidArgs},

		{name: "unlink", in: "/unlink", verb: "/unlink"},
		{name: "help", in: "/help", verb: "/help"},
		{name: "tickets", in: "/tickets", verb: "/tickets"},
		{name: "status", in: "/status", verb: "/status"},
		{name: "clear", in: "/clear", verb: "/clear"},

		{name: "link ok lowercase", in: "/link 123abc", verb: "/link", args: []string{"123abc"}},
		{name: "link ok uppercase hex", in: "/link DEADBE", verb: "/link", args: []string{"DEADBE"}},
		{name: "link no arg", in: "/link", wantErr: commands.ErrInvalidArgs},
		{name: "link non-hex", in: "/link xyz123", wantErr: commands.ErrInvalidArgs},
		{name: "link too short", in: "/link 123", wantErr: commands.ErrInvalidArgs},
		{name: "link too long", in: "/link 1234567", wantErr: commands.ErrInvalidArgs},
		{name: "link extra arg", in: "/link 123abc trailing", wantErr: commands.ErrInvalidArgs},

		{name: "autopilot start", in: "/autopilot start", verb: "/autopilot", args: []string{"start"}},
		{name: "autopilot stop", in: "/autopilot stop", verb: "/autopilot", args: []string{"stop"}},
		{name: "autopilot status", in: "/autopilot status", verb: "/autopilot", args: []string{"status"}},
		{name: "autopilot bogus", in: "/autopilot bogus", wantErr: commands.ErrInvalidArgs},
		{name: "autopilot no arg", in: "/autopilot", wantErr: commands.ErrInvalidArgs},
		{name: "autopilot extra args", in: "/autopilot start now", wantErr: commands.ErrInvalidArgs},

		{name: "use ok", in: "/use my-tenant", verb: "/use", args: []string{"my-tenant"}},
		{name: "use digits", in: "/use tenant-7", verb: "/use", args: []string{"tenant-7"}},
		{name: "use uppercase", in: "/use UPPER", wantErr: commands.ErrInvalidArgs},
		{name: "use underscore", in: "/use my_tenant", wantErr: commands.ErrInvalidArgs},
		{name: "use no arg", in: "/use", wantErr: commands.ErrInvalidArgs},

		{name: "dnd duration hours", in: "/dnd 2h", verb: "/dnd", args: []string{"2h"}},
		{name: "dnd duration seconds", in: "/dnd 30s", verb: "/dnd", args: []string{"30s"}},
		{name: "dnd duration compound", in: "/dnd 1h30m", verb: "/dnd", args: []string{"1h30m"}},
		{name: "dnd day shorthand 1d", in: "/dnd 1d", verb: "/dnd", args: []string{"1d"}},
		{name: "dnd day shorthand 7d", in: "/dnd 7d", verb: "/dnd", args: []string{"7d"}},
		{name: "dnd day shorthand 8d invalid", in: "/dnd 8d", wantErr: commands.ErrInvalidArgs},
		{name: "dnd no arg", in: "/dnd", wantErr: commands.ErrInvalidArgs},
		{name: "dnd garbage", in: "/dnd not-a-duration", wantErr: commands.ErrInvalidArgs},

		{name: "unknown verb", in: "/notreal", wantErr: commands.ErrUnknownCommand},
		{name: "unknown verb with args", in: "/notreal foo bar", wantErr: commands.ErrUnknownCommand},
		{name: "junk verb starts with digit", in: "/1bad", wantErr: commands.ErrUnknownCommand},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd, err := commands.Parse(tc.in)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("Parse(%q) err=%v, want %v", tc.in, err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Parse(%q) unexpected err=%v", tc.in, err)
			}
			if cmd.Verb != tc.verb {
				t.Fatalf("Parse(%q) verb=%q, want %q", tc.in, cmd.Verb, tc.verb)
			}
			if !equalStrings(cmd.Args, tc.args) {
				t.Fatalf("Parse(%q) args=%v, want %v", tc.in, cmd.Args, tc.args)
			}
		})
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
