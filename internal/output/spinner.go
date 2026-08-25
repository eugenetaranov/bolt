package output

import (
	"fmt"
	"time"
)

// spinnerFrames are the braille glyphs cycled while a task runs.
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

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
			fmt.Fprint(o.w, render(o.color(colorCyan, spinnerFrames[i%len(spinnerFrames)])))
			select {
			case <-s.stop:
				return
			case <-t.C:
			}
		}
	}()
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
