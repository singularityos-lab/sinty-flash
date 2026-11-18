package fastboot

import (
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

type Transport interface {
	Read(p []byte) (int, error)
	Write(p []byte) (int, error)
	Close() error
}

type Client struct {
	t    Transport
	Info func(string)
}

func New(t Transport) *Client { return &Client{t: t} }

func (c *Client) Close() error { return c.t.Close() }

const maxResponse = 256

func (c *Client) send(cmd string) error {
	if len(cmd) > 4096 {
		return fmt.Errorf("fastboot command too long: %d bytes", len(cmd))
	}
	_, err := c.t.Write([]byte(cmd))
	return err
}

func (c *Client) reply() (string, uint32, error) {
	buf := make([]byte, maxResponse)
	for {
		n, err := c.t.Read(buf)
		if err != nil {
			return "", 0, err
		}
		if n < 4 {
			return "", 0, fmt.Errorf("short fastboot reply %q", buf[:n])
		}
		kind, body := string(buf[:4]), string(buf[4:n])
		switch kind {
		case "OKAY":
			return body, 0, nil
		case "FAIL":
			return "", 0, &Failure{Message: body}
		case "DATA":
			size, err := strconv.ParseUint(body, 16, 32)
			if err != nil {
				return "", 0, fmt.Errorf("bad DATA reply %q", body)
			}
			return "", uint32(size), nil
		case "INFO", "TEXT":
			if c.Info != nil {
				c.Info(strings.TrimRight(body, "\n"))
			}
		default:
			return "", 0, fmt.Errorf("unknown fastboot reply %q", buf[:n])
		}
	}
}

type Failure struct{ Message string }

func (f *Failure) Error() string { return "device refused: " + f.Message }

func (c *Client) Command(cmd string) (string, error) {
	if err := c.send(cmd); err != nil {
		return "", err
	}
	body, size, err := c.reply()
	if err != nil {
		return "", err
	}
	if size != 0 {
		return "", fmt.Errorf("%s: device asked for %d bytes of data", cmd, size)
	}
	return body, nil
}

func (c *Client) GetVar(name string) (string, error) { return c.Command("getvar:" + name) }

func (c *Client) MaxDownload() (int64, error) {
	v, err := c.GetVar("max-download-size")
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseInt(strings.TrimPrefix(strings.ToLower(v), "0x"), 16, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("bad max-download-size %q", v)
	}
	return n, nil
}

const chunk = 1 << 20

func (c *Client) Download(r io.Reader, size int64) error {
	if size <= 0 || size > 0xffffffff {
		return fmt.Errorf("cannot download %d bytes", size)
	}
	if err := c.send(fmt.Sprintf("download:%08x", size)); err != nil {
		return err
	}
	_, want, err := c.reply()
	if err != nil {
		return err
	}
	if int64(want) != size {
		return fmt.Errorf("device accepted %d bytes, sending %d", want, size)
	}
	buf := make([]byte, chunk)
	left := size
	for left > 0 {
		n := int64(len(buf))
		if left < n {
			n = left
		}
		if _, err := io.ReadFull(r, buf[:n]); err != nil {
			return err
		}
		if _, err := c.t.Write(buf[:n]); err != nil {
			return err
		}
		left -= n
	}
	_, extra, err := c.reply()
	if err != nil {
		return err
	}
	if extra != 0 {
		return errors.New("device asked for more data after the download")
	}
	return nil
}

func (c *Client) Flash(partition string, r io.ReaderAt, size int64, progress func(done, total int64)) error {
	max, err := c.MaxDownload()
	if err != nil {
		return err
	}
	if size+int64(headerSize+3*chunkHeaderSize) <= max {
		if err := c.Download(io.NewSectionReader(r, 0, size), size); err != nil {
			return err
		}
		if _, err := c.Command("flash:" + partition); err != nil {
			return err
		}
		if progress != nil {
			progress(size, size)
		}
		return nil
	}
	pieces, err := Split(size, max)
	if err != nil {
		return err
	}
	var done int64
	for _, p := range pieces {
		img := p.Reader(r)
		if err := c.Download(img, p.Size()); err != nil {
			return err
		}
		if _, err := c.Command("flash:" + partition); err != nil {
			return err
		}
		done += p.Length
		if progress != nil {
			progress(done, size)
		}
	}
	return nil
}
