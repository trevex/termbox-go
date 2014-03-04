// +build !windows

package termbox

import "errors"
import "fmt"
import "os"
import "os/signal"
import "strings"
import "syscall"
import "runtime"

// public API

// Used to construct palettes from 24-bit RGB values
type RGB struct{ R, G, B byte }

// Loads various color palettes for 256-color terminals
func SetColorPalette(p []RGB) {
    for n, c := range p {
        out.WriteString(fmt.Sprintf("\033]4;%v;rgb:%2x/%2x/%2x\x1b\\", n, c.R, c.G, c.B))
    }
}

// A preconfigured palette corresponding to XTERM's defaults
var Palette256 []RGB

func init() {
	var r, g, b byte

	// initialize the standard 256 color palette
	Palette256 = make([]RGB, 256)

	// this is the default xterm palette for the first 16 colors
	// pay attention to the blues, which are increased in luma 
	// to compensate for human insensitivity to cooler colors

	r, g, b = 205, 205, 238
	Palette256[0] = RGB{0, 0, 0}
	Palette256[1] = RGB{r, 0, 0}
	Palette256[2] = RGB{0, g, 0}
	Palette256[3] = RGB{r, g, 0}
	Palette256[4] = RGB{0, 0, b}
	Palette256[5] = RGB{r, 0, b}
	Palette256[6] = RGB{0, g, b}
	Palette256[7] = RGB{r, g, b}

	r, g, b = 255, 255, 255
	Palette256[8] = RGB{127, 127, 127}
	Palette256[9] = RGB{r, 0, 0}
	Palette256[10] = RGB{0, g, 0}
	Palette256[11] = RGB{r, g, 0}
	Palette256[12] = RGB{92, 92, b}
	Palette256[13] = RGB{r, 0, b}
	Palette256[14] = RGB{0, g, b}
	Palette256[15] = RGB{r, g, b}


	// next we establish a 6x6x6 color cube with no blue
	// correction -- also xterm common, to the point that
	// many users think this is hardcoded
	c := 16

	for r = 0; r < 6; r++ {
		rr := r * 40
		if r > 0 {
			rr += 55
		}
		for g = 0; g < 6; g++ {
			gg := g * 40
			if g > 0 {
				gg += 55
			}
			for b = 0; b < 6; b++ {
				bb := b * 40
				if b > 0 {
					bb += 55
				}
				Palette256[c] = RGB{rr, gg, bb}
				c++
			}
		}
	}

	// and, following the user assumptions, this is
	// a 24 color grey ramp
	var v byte = 8
	for g := 0; g < 24; g++ {
		v += 10
		Palette256[c] = RGB{v, v, v}
		c++
	}
}

// instructs termbox to switch to either ColorMode16 or ColorMode256 
func SetColorMode(cm ColorMode) error {
	switch cm {
	case ColorMode16:
		color_mode = cm
		return nil
	case ColorMode256:
		// let it fall through, we need to examine $TERM
	default:
		return errors.New("termbox: invalid color mode requested")
	}

	term := os.Getenv("TERM")
	switch {
	case term == "":
		return errors.New("termbox: TERM environment variable not set")
	case strings.Index(term, "256") == -1:
		return errors.New("termbox: TERM does not contain \"256\"")
	}

	// this is the common palette expected by xterm-256 hackers; it is 
	// NOT the only possible one, and a SetColorPalette command might
	// be in order..

	color_mode = cm
	SetColorPalette(Palette256)
	return nil
}

// Initializes termbox library. This function should be called before any other functions.
// After successful initialization, the library must be finalized using 'Close' function.
//
// Example usage:
//      err := termbox.Init()
//      if err != nil {
//              panic(err)
//      }
//      defer termbox.Close()
func Init() error {
	var err error

	out, err = os.OpenFile("/dev/tty", syscall.O_WRONLY, 0)
	if err != nil {
		return err
	}
	in, err = syscall.Open("/dev/tty", syscall.O_RDONLY, 0)
	if err != nil {
		return err
	}

	err = setup_term()
	if err != nil {
		return fmt.Errorf("termbox: error while reading terminfo data: %v", err)
	}

	signal.Notify(sigwinch, syscall.SIGWINCH)
	signal.Notify(sigio, syscall.SIGIO)

	_, err = fcntl(in, syscall.F_SETFL, syscall.O_ASYNC|syscall.O_NONBLOCK)
	if err != nil {
		return err
	}
	_, err = fcntl(in, syscall.F_SETOWN, syscall.Getpid())
	if runtime.GOOS != "darwin" && err != nil {
		return err
	}
	err = tcgetattr(out.Fd(), &orig_tios)
	if err != nil {
		return err
	}

	tios := orig_tios
	tios.Iflag &^= syscall_IGNBRK | syscall_BRKINT | syscall_PARMRK |
		syscall_ISTRIP | syscall_INLCR | syscall_IGNCR |
		syscall_ICRNL | syscall_IXON
	tios.Oflag &^= syscall_OPOST
	tios.Lflag &^= syscall_ECHO | syscall_ECHONL | syscall_ICANON |
		syscall_ISIG | syscall_IEXTEN
	tios.Cflag &^= syscall_CSIZE | syscall_PARENB
	tios.Cflag |= syscall_CS8
	tios.Cc[syscall_VMIN] = 1
	tios.Cc[syscall_VTIME] = 0

	err = tcsetattr(out.Fd(), &tios)
	if err != nil {
		return err
	}

	out.WriteString(funcs[t_enter_ca])
	out.WriteString(funcs[t_enter_keypad])
	out.WriteString(funcs[t_hide_cursor])
	out.WriteString(funcs[t_clear_screen])

	termw, termh = get_term_size(out.Fd())
	back_buffer.init(termw, termh)
	front_buffer.init(termw, termh)
	back_buffer.clear()
	front_buffer.clear()

	go func() {
		buf := make([]byte, 128)
		for {
			select {
			case <-sigio:
				for {
					n, err := syscall.Read(in, buf)
					if err == syscall.EAGAIN || err == syscall.EWOULDBLOCK {
						break
					}
					select {
					case input_comm <- input_event{buf[:n], err}:
						ie := <-input_comm
						buf = ie.data[:128]
					case <-quit:
						return
					}
				}
			case <-quit:
				return
			}
		}
	}()

	return nil
}

// Finalizes termbox library, should be called after successful initialization
// when termbox's functionality isn't required anymore.
func Close() {
	quit <- 1
	out.WriteString(funcs[t_show_cursor])
	out.WriteString(funcs[t_sgr0])
	out.WriteString(funcs[t_clear_screen])
	out.WriteString(funcs[t_exit_ca])
	out.WriteString(funcs[t_exit_keypad])
	out.WriteString(funcs[t_exit_mouse])
	tcsetattr(out.Fd(), &orig_tios)

	out.Close()
	syscall.Close(in)

	// reset the state, so that on next Init() it will work again
	termw = 0
	termh = 0
	input_mode = InputEsc
	out = nil
	in = 0
	lastfg = attr_invalid
	lastbg = attr_invalid
	lastx = coord_invalid
	lasty = coord_invalid
	cursor_x = cursor_hidden
	cursor_y = cursor_hidden
	foreground = ColorDefault
	background = ColorDefault
}

// Synchronizes the internal back buffer with the terminal.
func Flush() error {
	// invalidate cursor position
	lastx = coord_invalid
	lasty = coord_invalid

	update_size_maybe()

	for y := 0; y < front_buffer.height; y++ {
		line_offset := y * front_buffer.width
		for x := 0; x < front_buffer.width; {
			cell_offset := line_offset + x
			back := &back_buffer.cells[cell_offset]
			front := &front_buffer.cells[cell_offset]
			if back.Ch < ' ' {
				back.Ch = ' '
			}
			w := rune_width(back.Ch)
			if *back == *front {
				x += w
				continue
			}
			*front = *back
			send_attr(back.Fg, back.Bg)

			if w == 2 && x == front_buffer.width-1 {
				// there's not enough space for 2-cells rune,
				// let's just put a space in there
				send_char(x, y, ' ')
			} else {
				send_char(x, y, back.Ch)
				if w == 2 {
					next := cell_offset + 1
					front_buffer.cells[next] = Cell{
						Ch: 0,
						Fg: back.Fg,
						Bg: back.Bg,
					}
				}
			}
			x += w
		}
	}
	if !is_cursor_hidden(cursor_x, cursor_y) {
		write_cursor(cursor_x, cursor_y)
	}
	return flush()
}

// Sets the position of the cursor. See also HideCursor().
func SetCursor(x, y int) {
	if is_cursor_hidden(cursor_x, cursor_y) && !is_cursor_hidden(x, y) {
		outbuf.WriteString(funcs[t_show_cursor])
	}

	if !is_cursor_hidden(cursor_x, cursor_y) && is_cursor_hidden(x, y) {
		outbuf.WriteString(funcs[t_hide_cursor])
	}

	cursor_x, cursor_y = x, y
	if !is_cursor_hidden(cursor_x, cursor_y) {
		write_cursor(cursor_x, cursor_y)
	}
}

// The shortcut for SetCursor(-1, -1).
func HideCursor() {
	SetCursor(cursor_hidden, cursor_hidden)
}

// Changes cell's parameters in the internal back buffer at the specified
// position.
func SetCell(x, y int, ch rune, fg, bg Attribute) {
	if x < 0 || x >= back_buffer.width {
		return
	}
	if y < 0 || y >= back_buffer.height {
		return
	}

	back_buffer.cells[y*back_buffer.width+x] = Cell{ch, fg, bg}
}

// Returns a slice into the termbox's back buffer. You can get its dimensions
// using 'Size' function. The slice remains valid as long as no 'Clear' or
// 'Flush' function calls were made after call to this function.
func CellBuffer() []Cell {
	return back_buffer.cells
}

// Wait for an event and return it. This is a blocking function call.
func PollEvent() Event {
	var event Event

	// try to extract event from input buffer, return on success
	event.Type = EventKey
	if extract_event(&event) {
		return event
	}

	for {
		select {
		case ev := <-input_comm:
			if ev.err != nil {
				return Event{Type: EventError, Err: ev.err}
			}

			inbuf = append(inbuf, ev.data...)
			input_comm <- ev
			if extract_event(&event) {
				return event
			}
		case <-sigwinch:
			event.Type = EventResize
			event.Width, event.Height = get_term_size(out.Fd())
			return event
		}
	}
	panic("unreachable")
}

// Returns the size of the internal back buffer (which is the same as
// terminal's window size in characters).
func Size() (int, int) {
	return termw, termh
}

// Clears the internal back buffer.
func Clear(fg, bg Attribute) error {
	foreground, background = fg, bg
	err := update_size_maybe()
	back_buffer.clear()
	return err
}

// Sets termbox input mode. Termbox has two input modes:
//
// 1. Esc input mode. When ESC sequence is in the buffer and it doesn't match
// any known sequence. ESC means KeyEsc. This is the default input mode.
//
// 2. Alt input mode. When ESC sequence is in the buffer and it doesn't match
// any known sequence. ESC enables ModAlt modifier for the next keyboard event.
//
// Both input modes can be OR'ed with Mouse mode. Setting Mouse mode bit up will
// enable mouse button click events.
//
// If 'mode' is InputCurrent, returns the current input mode. See also Input*
// constants.
func SetInputMode(mode InputMode) InputMode {
	if mode == InputCurrent {
		return input_mode
	}
	if mode&InputMouse != 0 {
		out.WriteString(funcs[t_enter_mouse])
	} else {
		out.WriteString(funcs[t_exit_mouse])
	}

	input_mode = mode
	return input_mode
}

// Sync comes handy when something causes desync between termbox's understanding
// of a terminal buffer and the reality. Such as a third party process. Sync
// forces a complete resync between the termbox and a terminal, it may not be
// visually pretty though.
func Sync() error {
	front_buffer.clear()
	err := send_clear()
	if err != nil {
		return err
	}

	return Flush()
}
