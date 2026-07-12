package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"syscall"
	"unsafe"
)

const (
	giloVersion = "0.0.1"

	ArrowLeft  rune = 'a'
	ArrowRight rune = 'd'
	ArrowUp    rune = 'w'
	ArrowDown  rune = 's'

	PageUp   rune
	PageDown rune
)

func enableRawMode() (*Termios, error) {
	originalTermios, err := GetAttr(uintptr(syscall.Stdin))
	if err != nil {
		return nil, err
	}
	raw := *originalTermios
	raw.Iflag &^= syscall.BRKINT | syscall.ICRNL | syscall.INPCK | syscall.ISTRIP | syscall.IXON
	raw.Oflag &^= syscall.OPOST
	raw.Lflag &^= syscall.ECHO | syscall.ICANON | syscall.IEXTEN | syscall.ISIG
	raw.Cflag |= syscall.CS8
	raw.Cc[syscall.VMIN] = 0
	raw.Cc[syscall.VTIME] = 1
	return originalTermios, raw.SetAttr(uintptr(syscall.Stdin))
}

// keysStatic just gives me a place to hold
// key related methods(functions). without
// breaking into another package.
type keysStatic struct{}

func (keysStatic) ctrlKey(c rune) rune {
	return c & 0x1f
}

var keys = keysStatic{}

type editor struct {
	termios *Termios

	input  *os.File //fixme: can these be an interface??
	output *os.File

	buf        []byte
	rows, cols int

	// cursor x and y
	cx, cy int
}

func newEditor(termios *Termios) *editor {
	var (
		err error
		e   = &editor{
			termios: termios,
			input:   os.Stdin,
			output:  os.Stdout,
			cx:      0,
			cy:      0,
		}
	)
	e.rows, e.cols, err = e.getWindowSize() // fixme: this isn't idiomatic
	if err != nil {
		panic(err)
	}
	return e
}

// similar to the abAppend in kilo
func (e *editor) Append(in []byte) {
	e.buf = append(e.buf, in...)
}

// with some convenience
func (e *editor) AppendString(in string) {
	e.Append([]byte(in))
}

func (e *editor) Write() error {
	_, err := os.Stdout.Write(e.buf)
	if err != nil {
		return err
	}
	e.buf = []byte{}
	return nil
}

func (e *editor) ReadKey() rune {
	var (
		buf = make([]byte, 1)
		n   int
		err error
	)
	for n != 1 {
		n, err = e.input.Read(buf)
		if err != nil && err != io.EOF {
			panic(err)
		}
	}

	if buf[0] == '\x1b' {
		escapeBuf := make([]byte, 2)
		n, err = e.input.Read(escapeBuf)
		if err != nil && err != io.EOF {
			panic(err)
		}

		if v, ok := map[string]rune{
			"[5~": PageUp,
			"[6~": PageDown,
			"[A":  ArrowUp,
			"[B":  ArrowDown,
			"[C":  ArrowRight,
			"[D":  ArrowLeft,
		}[string(escapeBuf)]; ok {
			return v
		}
		return rune('\x1b')
	}
	return rune(buf[0])
}

func (e *editor) getWindowSize() (int, int, error) {
	var ws struct {
		rows, cols uint16
	}
	err := ioctl(uintptr(syscall.Stdout), syscall.TIOCGWINSZ, unsafe.Pointer(&ws))
	if err != nil {
		// this is called before the editor "starts" no need to buffer here.
		_, writeErr := e.output.Write([]byte("\x1b[999C\x1b[999B"))
		if writeErr != nil {
			return 0, 0, fmt.Errorf("getting window size: %w after ioctl failure: %w", writeErr, err)
		}
		return e.getCursorPosition()
	}
	return int(ws.rows), int(ws.cols), nil
}

func (e *editor) drawRows() {
	for y := range e.rows {
		if y == e.rows/3 {
			welcome := fmt.Sprintf("gilo editor -- verison %s", giloVersion)
			padding := (e.cols - len(welcome)) / 2
			if padding > 0 {
				e.AppendString("~")
				padding--
			}
			for ; padding > 0; padding-- {
				e.AppendString(" ")
			}
			e.AppendString(welcome)
		} else {
			e.AppendString("~")
		}

		e.AppendString("\x1b[K")
		if y < e.rows-1 {
			e.AppendString("\r\n")
		}
	}
}

func (e *editor) refreshScreen() {
	e.AppendString("\x1b[?25l")
	e.AppendString("\x1b[H")
	e.drawRows()
	e.AppendString(fmt.Sprintf("\x1b[%d;%dH", e.cy+1, e.cx+1))
	e.AppendString("\x1b[?25h")
	e.Write()
}

func (e *editor) processKeypress(cancel context.CancelFunc) {
	c := e.ReadKey()
	switch c {
	case keys.ctrlKey('q'):
		// use output directly as we quit
		e.output.WriteString("\x1b[2J")
		e.output.WriteString("\x1b[H")
		e.output.WriteString("\x1b[?25h")
		cancel()

	case ArrowUp, ArrowLeft, ArrowDown, ArrowRight:
		e.editorMoveCursor(c)
	}
}

func (e editor) getCursorPosition() (int, int, error) {
	buf := make([]byte, 32)
	var rows, cols int

	n, err := os.Stdout.Write([]byte("\x1b[6n"))
	if n != 4 {
		err = fmt.Errorf("incomplete write")
	}
	if err != nil {
		return 0, 0, fmt.Errorf("writing to stdout: %w", err)
	}

	n, err = os.Stdin.Read(buf)
	if n > 0 {
		fmt.Sscanf(string(buf), "%d;%d", rows, cols)
	}
	if err != nil {
		return 0, 0, fmt.Errorf("reading from stdin: %w", err)
	}
	return rows, cols, nil
}

func (e *editor) editorMoveCursor(key rune) {
	switch key {
	case ArrowLeft:
		if e.cx != 0 {
			e.cx--
		}
	case ArrowRight:
		if e.cx != e.cols-1 {
			e.cx++
		}
	case ArrowUp:
		if e.cy != 0 {
			e.cy--
		}
	case ArrowDown:
		if e.cy != e.rows-1 {
			e.cy++
		}
	}
}

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	ot, err := enableRawMode()
	if err != nil {
		panic(err)
	}
	defer ot.SetAttr(uintptr(syscall.Stdin))

	e := newEditor(ot)

	for {
		e.refreshScreen()
		e.processKeypress(cancel)
		select {
		case <-ctx.Done():
			return
		default:
		}
	}
}
