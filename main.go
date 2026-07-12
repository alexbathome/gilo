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
	giloVersion           = "0.0.1"
	giloTabStop           = 4
	quitTimesDefault      = 3
	Backspace        rune = 127
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

// keysStatic holds key-related helper methods without a separate package.
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
	cx, cy, rx     int // cursor position; rx is the rendered x (tabs expand)

	statusMsg       string
	statusTime      time.Time
	dirty           int
	quitTimes       int
	lastMatch       int
	searchDirection rune
}

func newEditor(termios *Termios) *editor {
	var (
		err error
		e   = &editor{
			termios:         termios,
			input:           os.Stdin,
			output:          os.Stdout,
			quitTimes:       quitTimesDefault,
			lastMatch:       -1,
			searchDirection: ArrowRight,
		}
	)
	e.rows, e.cols, err = e.getWindowSize() // fixme: this isn't idiomatic
	if err != nil {
		panic(err)
	}
	e.rows -= 2 // status bar + message
	return e
}

// Append is similar to the abAppend buffer in kilo.
func (e *editor) Append(in []byte) {
	e.buf = append(e.buf, in...)
}

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

func (e *editor) rowDelChar(row *erow, at int) {
	if at < 0 || at >= len(row.chars) {
		return
	}
	row.chars = row.chars[:at] + row.chars[at+1:]
	e.updateRow(row)
}

func (e *editor) rowAppendString(row *erow, s string) {
	row.chars += s
	e.updateRow(row)
}

func (e *editor) insertRow(at int, chars string) {
	row := erow{chars: chars}
	e.updateRow(&row)
	e.erow = append(e.erow, erow{})
	copy(e.erow[at+1:], e.erow[at:])
	e.erow[at] = row
}

func (e *editor) delRow(at int) {
	if at < 0 || at >= len(e.erow) {
		return
	}
	e.erow = append(e.erow[:at], e.erow[at+1:]...)
}

func (e *editor) insertChar(c rune) {
	if e.cy == len(e.erow) {
		e.insertRow(len(e.erow), "")
	}
	e.rowInsertChar(&e.erow[e.cy], e.cx, c)
	e.cx++
	e.dirty++
}

func (e *editor) insertNewline() {
	if e.cx == 0 {
		e.insertRow(e.cy, "")
	} else {
		row := &e.erow[e.cy]
		e.insertRow(e.cy+1, row.chars[e.cx:])
		row = &e.erow[e.cy] // e.erow may have been reallocated by insertRow
		row.chars = row.chars[:e.cx]
		e.updateRow(row)
	}
	e.cy++
	e.cx = 0
	e.dirty++
}

func (e *editor) delChar() {
	if e.cy == len(e.erow) || (e.cx == 0 && e.cy == 0) {
		return
	}
	row := &e.erow[e.cy]
	if e.cx > 0 {
		e.rowDelChar(row, e.cx-1)
		e.cx--
	} else {
		prev := &e.erow[e.cy-1]
		e.cx = len(prev.chars)
		e.rowAppendString(prev, row.chars)
		e.delRow(e.cy)
		e.cy--
	}
	e.dirty++
}

func (e *editor) rowsToString() string {
	lines := make([]string, len(e.erow))
	for i, row := range e.erow {
		lines[i] = row.chars
	}
	return strings.Join(lines, "\n") + "\n"
}

func (e *editor) save() {
	if e.filename == "" {
		filename, ok := e.prompt("Save as: %s (ESC to cancel)", nil)
		if !ok {
			e.setStatusMessage("Save aborted")
			return
		}
		e.filename = filename
	}
	data := []byte(e.rowsToString())
	if err := os.WriteFile(e.filename, data, 0644); err != nil {
		e.setStatusMessage(fmt.Sprintf("Can't save! I/O error: %s", err))
		return
	}
	e.dirty = 0
	e.setStatusMessage(fmt.Sprintf("%d bytes written to disk", len(data)))
}

// readEscByte reads a single byte without retrying on timeout, so a lone ESC press can be distinguished from an escape sequence.
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

func (e *editor) rowRxToCx(row *erow, rx int) int {
	curRx, cx := 0, 0
	for cx = range row.chars {
		if row.chars[cx] == '\t' {
			curRx += giloTabStop - 1 - curRx%giloTabStop
		}
		curRx++
		if curRx > rx {
			return cx
		}
	}
	return len(row.chars)
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
	modified := ""
	if e.dirty > 0 {
		modified = " (modified)"
	}
	filestatus := fmt.Sprintf("%.20s - %d lines%s", filename, len(e.erow), modified)
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

// prompt reads a line of input on the status bar; callback (if non-nil) is
// invoked after every keypress so incremental search can jump to matches as you type.
func (e *editor) prompt(promptFmt string, callback func(query string, key rune)) (string, bool) {
	buf := ""
	for {
		e.setStatusMessage(fmt.Sprintf(promptFmt, buf))
		e.refreshScreen()
		c := e.ReadKey()
		switch {
		case c == Delete || c == Backspace || c == keys.ctrlKey('h'):
			if len(buf) > 0 {
				buf = buf[:len(buf)-1]
			}
		case c == '\x1b':
			e.setStatusMessage("")
			if callback != nil {
				callback(buf, c)
			}
			return "", false
		case c == '\r':
			if len(buf) != 0 {
				e.setStatusMessage("")
				if callback != nil {
					callback(buf, c)
				}
				return buf, true
			}
		case c >= 32 && c < 127:
			buf += string(c)
		}
		if callback != nil {
			callback(buf, c)
		}
	}
}

func (e *editor) findCallback(query string, key rune) {
	switch key {
	case '\r', '\x1b':
		e.lastMatch = -1
		e.searchDirection = ArrowRight
		return
	case ArrowRight, ArrowDown:
		e.searchDirection = ArrowRight
	case ArrowLeft, ArrowUp:
		e.searchDirection = ArrowLeft
	default:
		e.lastMatch = -1
		e.searchDirection = ArrowRight
	}
	if query == "" {
		return
	}
	current := e.lastMatch
	for range e.erow {
		if e.searchDirection == ArrowRight {
			current++
		} else {
			current--
		}
		switch {
		case current < 0:
			current = len(e.erow) - 1
		case current >= len(e.erow):
			current = 0
		}
		row := &e.erow[current]
		if idx := strings.Index(row.render, query); idx != -1 {
			e.lastMatch = current
			e.cy = current
			e.cx = e.rowRxToCx(row, idx)
			e.rowoff = len(e.erow) // force Scroll() to bring the match into view
			break
		}
	}
}

func (e *editor) find() {
	savedCx, savedCy := e.cx, e.cy
	savedColoff, savedRowoff := e.coloff, e.rowoff
	if _, ok := e.prompt("Search: %s (Use ESC/Arrows/Enter)", e.findCallback); !ok {
		e.cx, e.cy = savedCx, savedCy
		e.coloff, e.rowoff = savedColoff, savedRowoff
	}
}

func (e *editor) processKeypress(cancel context.CancelFunc) {
	c := e.ReadKey()
	if c != keys.ctrlKey('q') {
		e.quitTimes = quitTimesDefault
	}

	switch c {
	case '\r':
		e.insertNewline()
	case keys.ctrlKey('q'):
		if e.dirty > 0 && e.quitTimes > 0 {
			e.setStatusMessage(fmt.Sprintf("WARNING!!! File has unsaved changes. Press Ctrl-Q %d more times to quit.", e.quitTimes))
			e.quitTimes--
			return
		}
		// use output directly as we quit
		e.output.WriteString("\x1b[2J")
		e.output.WriteString("\x1b[H")
		e.output.WriteString("\x1b[?25h")
		cancel()
	case keys.ctrlKey('s'):
		e.save()
	case keys.ctrlKey('f'):
		e.find()
	case Home:
		e.cx = 0
	case End:
		if e.cy < len(e.erow) {
			e.cx = len(e.erow[e.cy].chars)
		}
	case Backspace, keys.ctrlKey('h'):
		e.delChar()
	case Delete:
		if e.cy < len(e.erow) {
			row := e.erow[e.cy]
			if e.cx < len(row.chars) || e.cy < len(e.erow)-1 {
				e.editorMoveCursor(ArrowRight)
				e.delChar()
			}
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
	case '\x1b', keys.ctrlKey('l'):
		// no-op: the screen already refreshes every loop iteration
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
	e.setStatusMessage("HELP: Ctrl-S = save | Ctrl-Q = quit | Ctrl-F = find")

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
