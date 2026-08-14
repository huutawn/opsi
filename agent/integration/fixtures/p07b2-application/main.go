package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type cacheConfig struct {
	host     string
	port     string
	user     string
	password string
}

func main() {
	config, err := loadCacheConfig(os.Getenv)
	if err != nil {
		log.Fatal(err)
	}
	if err := checkCache(config, true); err != nil {
		log.Fatal("cache startup check failed")
	}
	http.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		if checkCache(config, false) != nil {
			http.Error(w, "cache unavailable", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "ok\n")
	})
	log.Fatal(http.ListenAndServe(":8080", nil))
}

func loadCacheConfig(getenv func(string) string) (cacheConfig, error) {
	values := map[string]string{}
	for _, name := range []string{"CACHE_HOST", "CACHE_PORT", "CACHE_USER", "CACHE_PASSWORD", "CACHE_URL"} {
		values[name] = getenv(name)
		if values[name] == "" {
			return cacheConfig{}, fmt.Errorf("%s is required", name)
		}
	}
	if _, err := strconv.ParseUint(values["CACHE_PORT"], 10, 16); err != nil {
		return cacheConfig{}, errors.New("CACHE_PORT is invalid")
	}
	parsed, err := url.Parse(values["CACHE_URL"])
	if err != nil || parsed.Scheme != "redis" || parsed.User == nil {
		return cacheConfig{}, errors.New("CACHE_URL is invalid")
	}
	password, ok := parsed.User.Password()
	if !ok || parsed.Hostname() != values["CACHE_HOST"] || parsed.Port() != values["CACHE_PORT"] || parsed.User.Username() != values["CACHE_USER"] || password != values["CACHE_PASSWORD"] {
		return cacheConfig{}, errors.New("CACHE_URL does not match CACHE_HOST, CACHE_PORT, CACHE_USER, and CACHE_PASSWORD")
	}
	return cacheConfig{host: values["CACHE_HOST"], port: values["CACHE_PORT"], user: values["CACHE_USER"], password: values["CACHE_PASSWORD"]}, nil
}

func checkCache(config cacheConfig, exercise bool) error {
	connection, err := net.DialTimeout("tcp", net.JoinHostPort(config.host, config.port), 3*time.Second)
	if err != nil {
		return err
	}
	defer connection.Close()
	if err := connection.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		return err
	}
	reader := bufio.NewReader(connection)
	commands := [][]string{{"AUTH", config.user, config.password}, {"PING"}}
	if exercise {
		commands = append(commands, []string{"SET", "opsi-p07b2-acceptance", "bound"}, []string{"GET", "opsi-p07b2-acceptance"})
	}
	for index, command := range commands {
		if err := writeCommand(connection, command); err != nil {
			return err
		}
		response, err := readResponse(reader)
		if err != nil {
			return err
		}
		expected := "OK"
		if index == 1 {
			expected = "PONG"
		} else if exercise && index == 3 {
			expected = "bound"
		}
		if response != expected {
			return errors.New("unexpected cache response")
		}
	}
	return nil
}

func writeCommand(writer io.Writer, values []string) error {
	if _, err := fmt.Fprintf(writer, "*%d\r\n", len(values)); err != nil {
		return err
	}
	for _, value := range values {
		if _, err := fmt.Fprintf(writer, "$%d\r\n%s\r\n", len(value), value); err != nil {
			return err
		}
	}
	return nil
}

func readResponse(reader *bufio.Reader) (string, error) {
	line, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	line = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
	if len(line) < 1 {
		return "", errors.New("empty cache response")
	}
	switch line[0] {
	case '+':
		return line[1:], nil
	case '-':
		return "", errors.New("cache command failed")
	case '$':
		length, err := strconv.Atoi(line[1:])
		if err != nil || length < 0 {
			return "", errors.New("invalid cache bulk response")
		}
		value := make([]byte, length+2)
		if _, err := io.ReadFull(reader, value); err != nil {
			return "", err
		}
		return string(value[:length]), nil
	default:
		return "", errors.New("unsupported cache response")
	}
}
