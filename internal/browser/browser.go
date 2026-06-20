// Package browser opens a login URL on the host so the human's only step is to
// approve it — not to find and copy a URL out of streamed output. axprobe's own
// agentic experience should be good too.
package browser

import (
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"runtime"

	"github.com/segmentstream/axprobe/internal/events"
)

var urlRe = regexp.MustCompile(`https?://[^\s"']+`)

type opener struct {
	w      io.Writer
	buf    []byte
	opened bool
}

// TeeOpen returns a writer that forwards everything to w while watching the
// stream for the first complete URL and opening it in the host browser once
// (best-effort). Use it as the ExecStream target for an interactive login.
func TeeOpen(w io.Writer) io.Writer { return &opener{w: w} }

func (o *opener) Write(p []byte) (int, error) {
	n, err := o.w.Write(p)
	if !o.opened {
		o.buf = append(o.buf, p...)
		// Only act on a URL that is terminated (the match ends before the buffer
		// end), so we never open a partial URL split across stream chunks.
		if loc := urlRe.FindIndex(o.buf); loc != nil && loc[1] < len(o.buf) {
			o.opened = true
			url := string(o.buf[loc[0]:loc[1]])
			events.Emit("login_url", "url", url)
			open(url, o.w)
			o.buf = nil
		}
		if len(o.buf) > 16384 {
			o.buf = o.buf[len(o.buf)-4096:]
		}
	}
	return n, err
}

func open(url string, note io.Writer) {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", url)
	case "linux":
		cmd = exec.Command("xdg-open", url)
	default:
		return
	}
	go func() { _ = cmd.Run() }()
	fmt.Fprintln(note, "   ↗ opened the login URL in your browser — just approve it")
}
