# gilo 

gilo is a Go port of [Antirez's kilo](https://github.com/antirez/kilo), except it's less than 350 lines of `gofmt`'d code (counted with `wc -l main.go`) and 0 dependencies :) 

# disclaimer

1. gilo only provides minimal syntax highlighting for go files only. 
2. it cheaps out on calculating the window size, leaning on the `ioctl` [TIOCSWINSZ](https://man7.org/linux/man-pages/man2/TIOCSWINSZ.2const.html). This may or may not work with your terminal emulator. 
3. there's some pretty ugly code-golf smells in here, but oh well it's neat for < 350 lines :D

# usage 

```bash
go run github.com/alexbathome/gilo@latest
```

#### Key map

```
ctrl-q - quit
ctrl-f - find 
ctrl-s - save
```

#### example with highlighting 

```bash
curl -s https://raw.githubusercontent.com/alexbathome/gilo/refs/heads/main/main.go > /tmp/giloshowcase.go && go run github.com/alexbathome/gilo@latest /tmp/giloshowcase.go
```

#### example without highlighting: 

```bash
go run github.com/alexbathome/gilo@latest <(curl -s https://raw.githubusercontent.com/alexbathome/gilo/refs/heads/main/main.go)
```

it works on my mac, maybe it'll work on yours too 🤷❤️ 