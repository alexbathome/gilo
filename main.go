package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"
	"time"
	"unsafe"
)

const (
	ArrowLeft rune = iota + 1000 // start at 1000 so we don't overlap actual runes
	ArrowRight
	ArrowUp
	ArrowDown
	PageUp
	PageDown
	Home
	End
	Delete
)

const (
	giloVersion = "0.0.1"
	giloTabStop = 4
)

func enableRawMode() (*Termios, error) {
	originalTermios, err := GetAttr(uintptr(syscall.Stdin))
	if err != nil {
		return nil, err
	}
	raw := originalTermios.Clone()
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

type erow struct {
	chars, render string
}

type editor struct {
	termios *Termios

	input  *os.File //fixme: can these be an interface??
	output *os.File

	buf        []byte
	rows, cols int

	rowoff, coloff int
	erow           []erow
	filename       string

	// cursor (x,y), and rx (render)
	cx, cy, rx int

	statusMsg  string
	statusTime time.Time
}

func newEditor(termios *Termios) *editor {
	var (
		err error
		e   = &editor{
			termios: termios,
			input:   os.Stdin,
			output:  os.Stdout,
		}
	)
	e.rows, e.cols, err = e.getWindowSize() // fixme: this isn't idiomatic
	if err != nil {
		panic(err)
	}
	e.rows -= 2 // status bar + message
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

func (e *editor) updateRow(row *erow) {
	row.render = strings.ReplaceAll(row.chars, "\t", strings.Repeat(" ", giloTabStop))
}

func (e *editor) Open(filename string) error {
	e.filename = filename
	f, err := os.Open(filename)
	if err != nil {
		return fmt.Errorf("opening %s: %w", filename, err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return fmt.Errorf("scanning lines: %w", err)
		}
		text := scanner.Text()
		row := erow{chars: text}
		e.updateRow(&row)
		e.erow = append(e.erow, row)
	}
	return nil
}

func (e *editor) rowInsertChar(row *erow, at int, c rune) {
	row.chars = row.chars[:at] + string(c) + row.chars[at:]
	e.updateRow(row)
}

func (e *editor) insertChar(c rune) {
	if e.cy == len(e.erow) {
		e.erow = append(e.erow, erow{})
	}
	e.rowInsertChar(&e.erow[e.cy], e.cx, c)
	e.cx++
}

// readEscByte reads a single byte without retrying on timeout, so a lone
// ESC press (no follow-up bytes) can be distinguished from an escape sequence.
func (e *editor) readEscByte() (byte, bool) {
	buf := make([]byte, 1)
	n, err := e.input.Read(buf)
	if err != nil && err != io.EOF {
		panic(err)
	}
	return buf[0], n == 1
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

	if buf[0] != '\x1b' {
		return rune(buf[0])
	}

	seq0, ok := e.readEscByte()
	if !ok || seq0 != '[' {
		return rune('\x1b')
	}
	seq1, ok := e.readEscByte()
	if !ok {
		return rune('\x1b')
	}

	if seq1 >= '0' && seq1 <= '9' {
		seq2, ok := e.readEscByte()
		if !ok || seq2 != '~' {
			return rune('\x1b')
		}
		if v, ok := map[byte]rune{
			'1': Home,
			'3': Delete,
			'4': End,
			'5': PageUp,
			'6': PageDown,
		}[seq1]; ok {
			return v
		}
		return rune('\x1b')
	}

	if v, ok := map[byte]rune{
		'A': ArrowUp,
		'B': ArrowDown,
		'C': ArrowRight,
		'D': ArrowLeft,
		'H': Home,
		'F': End,
	}[seq1]; ok {
		return v
	}
	return rune('\x1b')
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

func (e *editor) editorRowCxtoRx(row *erow, cx int) int {
	rx := 0
	for j := range cx {
		if row.chars[j] == '\t' {
			rx += giloTabStop - 1 - rx%giloTabStop
		}
		rx++
	}
	return rx
}

func (e *editor) drawRows() {
	for y := range e.rows {
		filerow := y + e.rowoff
		if filerow >= len(e.erow) {
			if len(e.erow) == 0 && y == e.rows/3 {
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
		} else {
			row := e.erow[filerow]
			start := min(e.coloff, len(row.render))
			end := min(start+e.cols, len(row.render))
			e.AppendString(row.render[start:end])
		}

		e.AppendString("\x1b[K")
		e.AppendString("\r\n")
	}
}

func (e *editor) drawStatusBar() {
	var filename = "[No Name]"
	e.AppendString("\x1b[7m")
	if e.filename != "" {
		filename = e.filename
	}
	filestatus := fmt.Sprintf("%.20s - %d lines", filename, len(e.erow))
	if len(filestatus) > e.cols {
		filestatus = filestatus[:e.cols]
	}
	linenum := fmt.Sprintf("%d/%d", e.cy+1, len(e.erow))

	e.AppendString(filestatus)
	for l := len(filestatus); l < e.cols; l++ {
		if e.cols-l == len(linenum) {
			e.AppendString(linenum)
			break
		}
		e.AppendString(" ")
	}
	e.AppendString("\x1b[m")
	e.AppendString("\r\n")
}

func (e *editor) drawMessageBar() {
	e.AppendString("\x1b[K")
	end := min(len(e.statusMsg), e.cols)
	if time.Since(e.statusTime) < 5*time.Second {
		e.AppendString(e.statusMsg[:end])
	}
}

func (e *editor) refreshScreen() {
	e.Scroll()
	e.AppendString("\x1b[?25l")
	e.AppendString("\x1b[H")
	e.drawRows()
	e.drawStatusBar()
	e.drawMessageBar()
	e.AppendString(fmt.Sprintf("\x1b[%d;%dH", e.cy-e.rowoff+1, e.rx-e.coloff+1))
	e.AppendString("\x1b[?25h")
	e.Write()
}

func (e *editor) setStatusMessage(message string) {
	e.statusMsg = message
	e.statusTime = time.Now()
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

	case Home:
		e.cx = 0
	case End:
		if e.cy < len(e.erow) {
			e.cx = len(e.erow[e.cy].chars)
		}

	case PageUp, PageDown:
		var direction rune
		switch c {
		case PageUp:
			direction = ArrowUp
			e.cy = e.rowoff
		case PageDown:
			direction = ArrowDown
			e.cy = min(e.rowoff+e.rows-1, len(e.erow))
		}
		for times := e.rows; times > 0; times-- {
			e.editorMoveCursor(direction)
		}

	case ArrowUp, ArrowLeft, ArrowDown, ArrowRight:
		e.editorMoveCursor(c)

	default:
		e.insertChar(c)
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
	var row *string
	if e.cy < len(e.erow) {
		row = &e.erow[e.cy].chars
	}
	switch key {
	case ArrowLeft:
		if e.cx != 0 {
			e.cx--
		} else if e.cx > 0 {
			e.cy--
			e.cx = len(e.erow[e.cy].chars)
		}
	case ArrowRight:
		if row != nil && e.cx < len(*row) {
			e.cx++
		} else if row != nil && e.cx == len(*row) {
			e.cy++
			e.cx = 0
		}
	case ArrowUp:
		if e.cy != 0 {
			e.cy--
		}
	case ArrowDown:
		if e.cy < len(e.erow) {
			e.cy++
		}
	}

	if e.cy < len(e.erow) {
		row = &e.erow[e.cy].chars
	}
	if row != nil && e.cx > len(*row) {
		e.cx = len(*row)
	}
}

func (e *editor) Scroll() {
	e.rx = 0
	if e.cy < len(e.erow) {
		e.rx = e.editorRowCxtoRx(&e.erow[e.cy], e.cx)
	}
	if e.cy < e.rowoff {
		e.rowoff = e.cy
	}
	if e.cy >= e.rowoff+e.rows {
		e.rowoff = e.cy - e.rows + 1
	}
	if e.rx < e.coloff {
		e.coloff = e.rx
	}
	if e.rx >= e.coloff+e.cols {
		e.coloff = e.rx - e.cols + 1
	}
}

func main() {
	ctx, cancel := context.WithCancel(context.Background())
	ot, err := enableRawMode()
	if err != nil {
		panic(err)
	}
	defer ot.SetAttr(uintptr(syscall.Stdin))

	// enter the alternate screen buffer: keeps our redraws out of the
	// terminal's scrollback, and makes terminals forward PageUp/PageDown
	// and fn-arrow combos to us instead of using them to scroll history.
	os.Stdout.WriteString("\x1b[?1049h")
	defer os.Stdout.WriteString("\x1b[?1049l")

	e := newEditor(ot)
	if len(os.Args) >= 2 {
		e.Open(os.Args[1])
	}

	e.setStatusMessage("HELP: Ctrl-Q = quit")

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
