package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"net/url"
	"strconv"
	"testing"
)

func TestFixtureRequiresCanonicalBindingAndExercisesValkey(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	done := make(chan error, 1)
	go func() {
		connection, err := listener.Accept()
		if err != nil {
			done <- err
			return
		}
		defer connection.Close()
		reader := bufio.NewReader(connection)
		for _, expected := range [][]string{{"AUTH", "opsi", "secret"}, {"PING"}, {"SET", "opsi-p07b2-acceptance", "bound"}, {"GET", "opsi-p07b2-acceptance"}} {
			command, err := readCommand(reader)
			if err != nil || fmt.Sprint(command) != fmt.Sprint(expected) {
				done <- fmt.Errorf("command=%v expected=%v err=%v", command, expected, err)
				return
			}
			response := "+OK\r\n"
			if expected[0] == "PING" {
				response = "+PONG\r\n"
			} else if expected[0] == "GET" {
				response = "$5\r\nbound\r\n"
			}
			if _, err := io.WriteString(connection, response); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	host, port, _ := net.SplitHostPort(listener.Addr().String())
	values := map[string]string{
		"CACHE_HOST": host, "CACHE_PORT": port, "CACHE_USER": "opsi", "CACHE_PASSWORD": "secret",
		"CACHE_URL": "redis://opsi:" + url.QueryEscape("secret") + "@" + listener.Addr().String(),
	}
	config, err := loadCacheConfig(func(name string) string { return values[name] })
	if err != nil {
		t.Fatal(err)
	}
	if err := checkCache(config, true); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	delete(values, "CACHE_PASSWORD")
	if _, err := loadCacheConfig(func(name string) string { return values[name] }); err == nil {
		t.Fatal("missing canonical binding value was accepted")
	}
}

func readCommand(reader *bufio.Reader) ([]string, error) {
	line, err := reader.ReadString('\n')
	if err != nil {
		return nil, err
	}
	count, err := strconv.Atoi(line[1 : len(line)-2])
	if err != nil {
		return nil, err
	}
	values := make([]string, 0, count)
	for range count {
		line, err = reader.ReadString('\n')
		if err != nil {
			return nil, err
		}
		length, err := strconv.Atoi(line[1 : len(line)-2])
		if err != nil {
			return nil, err
		}
		value := make([]byte, length+2)
		if _, err := io.ReadFull(reader, value); err != nil {
			return nil, err
		}
		values = append(values, string(value[:length]))
	}
	return values, nil
}
