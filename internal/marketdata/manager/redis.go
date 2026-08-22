package manager

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// redisConn is a minimal Redis client using the RESP protocol.
// It supports GET, SET, SETEX, DEL, and PING — enough for caching.
// No third-party Redis library dependency.
type redisConn struct {
	mu     sync.Mutex
	conn   net.Conn
	reader *bufio.Reader
}

func dialRedis(rawURL string) (*redisConn, error) {
	addr, db, err := parseRedisURL(rawURL)
	if err != nil {
		return nil, err
	}

	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		return nil, err
	}

	rc := &redisConn{
		conn:   conn,
		reader: bufio.NewReader(conn),
	}

	// Select DB if not 0
	if db > 0 {
		if err := rc.selectDB(db); err != nil {
			conn.Close()
			return nil, err
		}
	}

	return rc, nil
}

func parseRedisURL(rawURL string) (addr string, db int, err error) {
	// Handle redis://host:port/db format
	if strings.HasPrefix(rawURL, "redis://") {
		u, err := url.Parse(rawURL)
		if err != nil {
			return "", 0, err
		}
		host := u.Hostname()
		port := u.Port()
		if port == "" {
			port = "6379"
		}
		addr = host + ":" + port
		// Parse DB from path
		path := strings.TrimPrefix(u.Path, "/")
		if path != "" {
			db, _ = strconv.Atoi(path)
		}
		return addr, db, nil
	}
	// Plain host:port
	if !strings.Contains(rawURL, ":") {
		rawURL += ":6379"
	}
	return rawURL, 0, nil
}

func (r *redisConn) ping() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.writeCommand("PING"); err != nil {
		return err
	}
	resp, err := r.readResponse()
	if err != nil {
		return err
	}
	if resp != "PONG" && resp != "+PONG" {
		return fmt.Errorf("unexpected ping response: %s", resp)
	}
	return nil
}

func (r *redisConn) get(key string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.writeCommand("GET", key); err != nil {
		return nil, err
	}
	return r.readBulk()
}

func (r *redisConn) set(key string, value []byte) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.writeCommandBytes("SET", key, value); err != nil {
		return err
	}
	_, err := r.readResponse()
	return err
}

func (r *redisConn) setex(key string, value []byte, ttl time.Duration) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	seconds := int(ttl.Seconds())
	if seconds < 1 {
		seconds = 1
	}
	if err := r.writeCommandBytesWithArg("SETEX", key, strconv.Itoa(seconds), value); err != nil {
		return err
	}
	_, err := r.readResponse()
	return err
}

func (r *redisConn) del(key string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.writeCommand("DEL", key); err != nil {
		return err
	}
	_, err := r.readResponse()
	return err
}

func (r *redisConn) selectDB(db int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.writeCommand("SELECT", strconv.Itoa(db)); err != nil {
		return err
	}
	_, err := r.readResponse()
	return err
}

func (r *redisConn) close() error {
	return r.conn.Close()
}

// ---------- RESP protocol ----------

func (r *redisConn) writeCommand(args ...string) error {
	// RESP array: *<count>\r\n followed by $<len>\r\n<data>\r\n for each arg
	var buf strings.Builder
	buf.WriteString(fmt.Sprintf("*%d\r\n", len(args)))
	for _, arg := range args {
		buf.WriteString(fmt.Sprintf("$%d\r\n%s\r\n", len(arg), arg))
	}
	_ = r.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_, err := r.conn.Write([]byte(buf.String()))
	return err
}

func (r *redisConn) writeCommandBytes(cmd, key string, value []byte) error {
	header := fmt.Sprintf("*3\r\n$%d\r\n%s\r\n$%d\r\n%s\r\n$%d\r\n",
		len(cmd), cmd, len(key), key, len(value))
	_ = r.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err := r.conn.Write([]byte(header)); err != nil {
		return err
	}
	if _, err := r.conn.Write(value); err != nil {
		return err
	}
	_, err := r.conn.Write([]byte("\r\n"))
	return err
}

func (r *redisConn) writeCommandBytesWithArg(cmd, key, arg string, value []byte) error {
	header := fmt.Sprintf("*4\r\n$%d\r\n%s\r\n$%d\r\n%s\r\n$%d\r\n%s\r\n$%d\r\n",
		len(cmd), cmd, len(key), key, len(arg), arg, len(value))
	_ = r.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err := r.conn.Write([]byte(header)); err != nil {
		return err
	}
	if _, err := r.conn.Write(value); err != nil {
		return err
	}
	_, err := r.conn.Write([]byte("\r\n"))
	return err
}

func (r *redisConn) readResponse() (string, error) {
	_ = r.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := r.reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	line = strings.TrimRight(line, "\r\n")
	if len(line) == 0 {
		return "", errors.New("empty redis response")
	}
	switch line[0] {
	case '+': // Simple string
		return line[1:], nil
	case '-': // Error
		return "", fmt.Errorf("redis error: %s", line[1:])
	case ':': // Integer
		return line[1:], nil
	case '$': // Bulk string (handled by readBulk)
		return line, nil
	case '*': // Array
		return line, nil
	}
	return line, nil
}

func (r *redisConn) readBulk() ([]byte, error) {
	_ = r.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	line, err := r.reader.ReadString('\n')
	if err != nil {
		return nil, err
	}
	line = strings.TrimRight(line, "\r\n")
	if len(line) == 0 {
		return nil, errors.New("empty redis bulk response")
	}
	if line[0] == '-' {
		return nil, fmt.Errorf("redis error: %s", line[1:])
	}
	if line[0] != '$' {
		// Simple string response (e.g. from SET)
		if line[0] == '+' {
			return []byte(line[1:]), nil
		}
		return nil, fmt.Errorf("unexpected redis response: %s", line)
	}
	size, err := strconv.Atoi(line[1:])
	if err != nil {
		return nil, fmt.Errorf("invalid bulk size: %s", line)
	}
	if size == -1 {
		return nil, nil // nil bulk string = key not found
	}
	data := make([]byte, size+2) // +2 for trailing \r\n
	n := 0
	for n < len(data) {
		read, err := r.reader.Read(data[n:])
		if err != nil {
			return nil, err
		}
		n += read
	}
	return data[:size], nil
}
