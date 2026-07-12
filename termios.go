package main

import (
	"syscall"
	"unsafe"
)

// Termios struct reporesents the termios struct in termios.h
// as defined in "golang.org/x/sys/unix"
type Termios struct {
	Iflag  uint64
	Oflag  uint64
	Cflag  uint64
	Lflag  uint64
	Cc     [20]uint8
	Ispeed uint64
	Ospeed uint64
}

// TermiosState wraps the live termios along with the original settings
// captured at startup so they can be restored later.
type TermiosState struct {
	original *Termios
	live     Termios
}

func NewTermios() (*TermiosState, error) {
	original, err := getAttr(uintptr(syscall.Stdin))
	if err != nil {
		return nil, err
	}
	return &TermiosState{
		original: original,
		live:     original.clone(),
	}, nil
}

// setAttr sets the attributes of the given terminal from this termios
// structure, immediately. See tcsetattr(3).
func (tio *Termios) setAttr(fd uintptr) error {
	return ioctl(fd, syscall.TIOCSETAF, unsafe.Pointer(tio))
}

func (ts *TermiosState) setRaw() error {
	tio := &ts.live
	tio.Iflag &^= syscall.BRKINT | syscall.ICRNL | syscall.INPCK | syscall.ISTRIP | syscall.IXON
	tio.Oflag &^= syscall.OPOST
	tio.Lflag &^= syscall.ECHO | syscall.ICANON | syscall.IEXTEN | syscall.ISIG
	tio.Cflag |= syscall.CS8
	tio.Cc[syscall.VMIN] = 0
	tio.Cc[syscall.VTIME] = 1
	return tio.setAttr(uintptr(syscall.Stdin))
}

func (ts *TermiosState) reset() error {
	return ts.original.setAttr(uintptr(syscall.Stdin))
}

func (tio *Termios) clone() Termios {
	return *tio
}

func ioctl(fd, cmd uintptr, arg unsafe.Pointer) error {
	return ioctlu(fd, cmd, uintptr(arg))
}

func ioctlu(fd, cmd, arg uintptr) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, cmd, arg)
	if errno == 0 {
		return nil
	}
	return errno
}

// getAttr gets the attributes of the given terminal.
func getAttr(fd uintptr) (*Termios, error) {
	var tio Termios
	if err := ioctl(fd, syscall.TIOCGETA, unsafe.Pointer(&tio)); err != nil {
		return nil, err
	}

	return &tio, nil
}
