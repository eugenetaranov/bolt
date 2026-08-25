package output

import (
	"fmt"
	"time"
)

// spinnerFrames are the braille glyphs cycled while a task runs.
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// Bright ANSI colors used by the "flower" style (safe: it only renders when
// spinnerOn(), which already requires color output).
const (
	colorBrightCyan    = "\033[96m"
	colorBrightWhite   = "\033[97m"
	colorBrightMagenta = "\033[95m"
	colorBrightYellow  = "\033[93m"
)

// flowerFrames bloom a single glyph from a dot to an open flower/burst and back.
var flowerFrames = []string{"·", "✢", "✦", "✳", "✺", "❋", "✺", "✳", "✦", "✢"}

// sparkleColors cycle to give the flower a firework shimmer.
var sparkleColors = []string{colorBrightCyan, colorBrightWhite, colorBrightMagenta, colorBrightYellow}

// glyph returns the animated spinner glyph for tick i in the active style:
// the default braille "dots", or "flower" — a blooming, color-cycling burst.
func (o *Output) glyph(i int) string {
	if o.spinStyle == "flower" {
		return sparkleColors[(i/2)%len(sparkleColors)] + flowerFrames[i%len(flowerFrames)] + colorReset
	}
	return o.color(colorCyan, spinnerFrames[i%len(spinnerFrames)])
}

// spinner holds the lifecycle channels for an in-progress spinner animation.
type spinner struct {
	stop chan struct{}
	done chan struct{}
}

// startSpinner renders an animated frame followed by name on the current line,
// updating in place until stopSpinner is called. Only used in interactive mode.
func (o *Output) startSpinner(name string) {
	o.startSpinnerRender(func(frame string) string {
		return fmt.Sprintf("\r  %s %s\033[K", frame, name)
	})
}

// startLineSpinner animates a spinner at the END of prefix, rewriting the whole
// line in place. Used for the host fact-gathering banner
// ("HOST h [ssh] - gathering facts ⠋"). Only used in interactive mode.
func (o *Output) startLineSpinner(prefix string) {
	o.startSpinnerRender(func(frame string) string {
		return fmt.Sprintf("\r%s %s\033[K", prefix, frame)
	})
}

// startSpinnerRender runs the spinner animation loop, writing render(frame)
// each tick until stopSpinner is called. frame is the colored glyph.
func (o *Output) startSpinnerRender(render func(frame string) string) {
	s := &spinner{stop: make(chan struct{}), done: make(chan struct{})}
	o.spin = s
	go func() {
		defer close(s.done)
		t := time.NewTicker(90 * time.Millisecond)
		defer t.Stop()
		for i := 0; ; i++ {
			fmt.Fprint(o.w, render(o.glyph(i)))
			select {
			case <-s.stop:
				return
			case <-t.C:
			}
		}
	}()
}

// StartProgress animates a spinner with a dynamic label (labelFn is called
// each frame, e.g. to show a live "4/10 hosts" count) and returns a stop
// function that halts the animation and clears the line. It is a no-op (stop
// does nothing) outside interactive mode, so CI/piped output stays quiet.
// Only one spinner may run at a time.
func (o *Output) StartProgress(labelFn func() string) (stop func()) {
	if !o.spinnerOn() {
		return func() {}
	}
	o.startSpinnerRender(func(frame string) string {
		return fmt.Sprintf("\r%s %s\033[K", frame, labelFn())
	})
	return func() {
		o.stopSpinner()
		fmt.Fprint(o.w, "\r\033[K") // clear the transient progress line
	}
}

// stopSpinner halts the animation goroutine and joins it, guaranteeing no
// further writes to o.w happen from the spinner before the caller writes next.
func (o *Output) stopSpinner() {
	if o.spin == nil {
		return
	}
	close(o.spin.stop)
	<-o.spin.done
	o.spin = nil
}
