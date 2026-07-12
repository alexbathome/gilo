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

func (tio *Termios) Clone() Termios {
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

// GetAttr gets the attributes of the given terminal.
func GetAttr(fd uintptr) (*Termios, error) {
	var tio Termios
	if err := ioctl(fd, syscall.TIOCGETA, unsafe.Pointer(&tio)); err != nil {
		return nil, err
	}

	return &tio, nil
}

// SetAttr sets the attributes of the given terminal from this termios
// structure, immediately. See tcsetattr(3).
func (tio *Termios) SetAttr(fd uintptr) error {
	return ioctl(fd, syscall.TIOCSETAF, unsafe.Pointer(tio))
}
