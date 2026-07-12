package main

import (
	"bufio"
	"fmt"
	"go/scanner"
	"go/token"
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

	giloTabStop           = 4
	quitTimesDefault      = 3
	Backspace        rune = 127
)

// editor state: the buffer is one string with tabs pre-expanded, the cursor is a byte offset into it.
var (
	buf            []byte // pending screen writes, flushed once per refresh
	text           string
	off            int
	rows, cols     int
	rowoff, coloff int
	filename       string
	statusMsg      string
	statusTime     time.Time
	dirty          int
	quitTimes      = quitTimesDefault
	buffer         = bufio.NewReader(os.Stdin)
	tab            = strings.Repeat(" ", giloTabStop)
	escSeqKeys     = map[rune]rune{'1': Home, '3': Delete, '4': End, '5': PageUp, '6': PageDown, 'A': ArrowUp, 'B': ArrowDown, 'C': ArrowRight, 'D': ArrowLeft, 'H': Home, 'F': End}
	tokColor       = map[token.Token]string{token.COMMENT: "36", token.STRING: "35", token.CHAR: "35", token.INT: "31", token.FLOAT: "31", token.IMAG: "31"}
	keymap         = map[rune]func(){'\r': func() { insert("\n") }, ctrlKey('s'): save, ctrlKey('f'): find, Backspace: del, ctrlKey('h'): del}
)

// ternary evaluates both branches eagerly, don't pass args that panic when unchosen.
func ternary[T any](thing bool, this T, otherwise T) T {
	if thing {
		return this
	}
	return otherwise
}

func clampOffset(pos int, offset *int, size int) {
	if pos < *offset {
		*offset = pos
	}
	if pos >= *offset+size {
		*offset = pos - size + 1
	}
}

// Termios mirrors struct termios from termios.h (darwin layout).
type Termios struct {
	Iflag, Oflag, Cflag, Lflag uint64
	Cc                         [20]uint8
	Ispeed, Ospeed             uint64
}

var origTermios Termios

func ioctl(fd, cmd uintptr, arg unsafe.Pointer) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, cmd, uintptr(arg))
	return ternary(errno == 0, error(nil), error(errno))
}

func setRaw() error {
	if err := ioctl(uintptr(syscall.Stdin), syscall.TIOCGETA, unsafe.Pointer(&origTermios)); err != nil {
		return err
	}
	tio := origTermios
	tio.Iflag &^= syscall.BRKINT | syscall.ICRNL | syscall.INPCK | syscall.ISTRIP | syscall.IXON
	tio.Oflag &^= syscall.OPOST
	tio.Lflag &^= syscall.ECHO | syscall.ICANON | syscall.IEXTEN | syscall.ISIG
	tio.Cflag |= syscall.CS8
	tio.Cc[syscall.VMIN], tio.Cc[syscall.VTIME] = 1, 0 // block for one byte; no timeout ticks
	return ioctl(uintptr(syscall.Stdin), syscall.TIOCSETAF, unsafe.Pointer(&tio))
}

func ctrlKey(c rune) rune {
	return c & 0x1f
}

// highlight wraps Go tokens in ANSI colors. Line-local: multi-line strings/comments won't carry across rows.
func highlight(line string) string {
	fset := token.NewFileSet()
	var s scanner.Scanner
	s.Init(fset.AddFile("", 1, len(line)), []byte(line), nil, scanner.ScanComments)
	hl, prev := "", 0
	for pos, tok, lit := s.Scan(); tok != token.EOF; pos, tok, lit = s.Scan() {
		start, length := int(pos)-1, len(ternary(lit != "", lit, tok.String()))
		color := ternary(tok.IsKeyword(), "33", tokColor[tok])
		if color == "" || start < prev || start >= len(line) {
			continue
		}
		end := min(start+length, len(line))
		hl += line[prev:start] + "\x1b[" + color + "m" + line[start:end] + "\x1b[39m"
		prev = end
	}
	return hl + line[prev:]
}

func bol(i int) int {
	return strings.LastIndex(text[:i], "\n") + 1
}

func eol(i int) int {
	j := strings.IndexByte(text[i:], '\n')
	return ternary(j < 0, len(text), i+j)
}

func insert(s string) {
	text = text[:off] + s + text[off:]
	off += len(s)
	dirty++
}

func del() {
	if off > 0 {
		text = text[:off-1] + text[off:]
		off--
		dirty++
	}
}

func save() {
	if filename == "" {
		name, ok := prompt("Save as: %s (ESC to cancel)", nil)
		if !ok {
			setStatusMessage("Save aborted")
			return
		}
		filename = name
	}
	data := ternary(text == "" || strings.HasSuffix(text, "\n"), text, text+"\n")
	if err := os.WriteFile(filename, []byte(data), 0644); err != nil {
		setStatusMessage(fmt.Sprintf("Can't save! I/O error: %s", err))
		return
	}
	dirty = 0
	setStatusMessage(fmt.Sprintf("%d bytes written to disk", len(data)))
}

// readKey blocks for a key. A real escape sequence arrives as one burst, so ESC with nothing buffered is a lone ESC press.
func readKey() rune {
	c, _, err := buffer.ReadRune()
	if err != nil || c != '\x1b' || buffer.Buffered() == 0 {
		return ternary(err == nil, c, '\x1b')
	}
	if b, _ := buffer.ReadByte(); b != '[' {
		return '\x1b'
	}
	seq, _ := buffer.ReadByte()
	if seq >= '0' && seq <= '9' {
		if b, _ := buffer.ReadByte(); b != '~' {
			return '\x1b'
		}
	}
	return ternary(escSeqKeys[rune(seq)] != 0, escSeqKeys[rune(seq)], '\x1b')
}

func out(in string) {
	buf = append(buf, []byte(in)...)
}

func refresh() {
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	cy, cx := strings.Count(text[:off], "\n"), off-bol(off)
	clampOffset(cy, &rowoff, rows)
	clampOffset(cx, &coloff, cols)
	out("\x1b[?25l\x1b[H")
	for y := range rows {
		if r := y + rowoff; r < len(lines) {
			line := lines[r]
			s := min(coloff, len(line))
			e := min(s+cols, len(line))
			out(ternary(strings.HasSuffix(filename, ".go"), highlight(line[s:e]), line[s:e]))
		} else {
			out("~")
		}
		out("\x1b[K\r\n")
	}
	name := ternary(filename != "", filename, "[No name]")
	status := fmt.Sprintf("%.20s - %d lines%s", name, len(lines), ternary(dirty > 0, " (modified)", ""))
	status = status[:min(len(status), cols)]
	linenum := fmt.Sprintf("%d/%d", cy+1, len(lines))
	pad := cols - len(status) - len(linenum)
	out("\x1b[7m" + status + ternary(pad >= 0, strings.Repeat(" ", max(pad, 0))+linenum, strings.Repeat(" ", cols-len(status))) + "\x1b[m\r\n\x1b[K")
	if time.Since(statusTime) < 5*time.Second {
		out(statusMsg[:min(len(statusMsg), cols)])
	}
	out(fmt.Sprintf("\x1b[%d;%dH\x1b[?25h", cy-rowoff+1, cx-coloff+1))
	os.Stdout.Write(buf)
	buf = buf[:0]
}

func setStatusMessage(message string) {
	statusMsg = message
	statusTime = time.Now()
}

// prompt reads a line on the status bar; callback (nil ok) runs after every keypress so search can jump as you type.
func prompt(promptFmt string, callback func(query string, key rune)) (string, bool) {
	if callback == nil {
		callback = func(string, rune) {}
	}
	line := ""
	for {
		setStatusMessage(fmt.Sprintf(promptFmt, line))
		refresh()
		c := readKey()
		switch {
		case c == Delete || c == Backspace || c == ctrlKey('h'):
			line = line[:max(len(line)-1, 0)]
		case c == '\x1b':
			setStatusMessage("")
			callback(line, c)
			return "", false
		case c == '\r' && line != "":
			setStatusMessage("")
			callback(line, c)
			return line, true
		case c >= 32 && c < 127:
			line += string(c)
		}
		callback(line, c)
	}
}

func findCallback(query string, key rune) {
	if query == "" || key == '\r' || key == '\x1b' {
		return
	}
	if key == ArrowLeft || key == ArrowUp {
		if i := strings.LastIndex(text[:off], query); i >= 0 {
			off = i
		} else if i := strings.LastIndex(text, query); i >= 0 {
			off = i
		}
		return
	}
	pos := ternary(key == ArrowRight || key == ArrowDown, min(off+1, len(text)), 0)
	if i := strings.Index(text[pos:], query); i >= 0 {
		off = pos + i
	} else if i := strings.Index(text, query); i >= 0 {
		off = i
	}
}

func find() {
	savedOff, savedRow, savedCol := off, rowoff, coloff
	if _, ok := prompt("Search: %s (Use ESC/Arrows/Enter)", findCallback); !ok {
		off, rowoff, coloff = savedOff, savedRow, savedCol
	}
}

func move(key rune) {
	switch key {
	case ArrowLeft:
		off = max(off-1, 0)
	case ArrowRight:
		off = min(off+1, len(text))
	case Home:
		off = bol(off)
	case End:
		off = eol(off)
	case Delete:
		if off < len(text) {
			off++
			del()
		}
	case PageUp, PageDown:
		for range rows {
			move(ternary(key == PageUp, ArrowUp, ArrowDown))
		}
	case ArrowUp, ArrowDown:
		col := off - bol(off)
		if key == ArrowUp && bol(off) > 0 {
			off = min(bol(bol(off)-1)+col, bol(off)-1)
		} else if key == ArrowDown && eol(off) < len(text) {
			off = min(eol(off)+1+col, eol(eol(off)+1))
		}
	}
}

func processKeypress() bool {
	c := readKey()
	if c != ctrlKey('q') {
		quitTimes = quitTimesDefault
	} else if dirty > 0 && quitTimes > 0 {
		setStatusMessage(fmt.Sprintf("WARNING!!! File has unsaved changes. Press Ctrl-Q %d more times to quit.", quitTimes))
		quitTimes--
		return false
	} else {
		os.Stdout.WriteString("\x1b[2J\x1b[H\x1b[?25h")
		return true
	}
	switch f, ok := keymap[c]; {
	case ok:
		f()
	case c >= ArrowLeft:
		move(c)
	case c >= 32 || c == '\t':
		insert(ternary(c == '\t', tab, string(c)))
	}
	return false
}

func main() {
	var ws struct{ rows, cols, x, y uint16 } // struct winsize: kernel writes all four fields
	if err := ioctl(uintptr(syscall.Stdout), syscall.TIOCGWINSZ, unsafe.Pointer(&ws)); err != nil {
		panic(err)
	}
	rows, cols = int(ws.rows)-2, int(ws.cols) // -2: status bar + message
	if err := setRaw(); err != nil {
		panic(err)
	}
	defer ioctl(uintptr(syscall.Stdin), syscall.TIOCSETAF, unsafe.Pointer(&origTermios))
	os.Stdout.WriteString("\x1b[?1049h") // alternate screen: keeps redraws out of scrollback
	defer os.Stdout.WriteString("\x1b[?1049l")
	if len(os.Args) >= 2 {
		filename = os.Args[1]
		data, _ := os.ReadFile(filename)
		text = strings.ReplaceAll(string(data), "\t", tab)
	}
	setStatusMessage("HELP: Ctrl-S = save | Ctrl-Q = quit | Ctrl-F = find")
	for {
		refresh()
		if processKeypress() {
			return
		}
	}
}
