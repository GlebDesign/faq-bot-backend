package main

import (
	"bytes"
	"net"
	"net/http"
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func TestSIGTERM(t *testing.T) {
	if os.Getenv("FAQ_API_TEST_CHILD") == "1" {
		main()
		return
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	cmd := exec.Command(os.Args[0], "-test.run=^TestSIGTERM$")
	oldURL, hadURL := os.LookupEnv("DATABASE_URL")
	t.Cleanup(func() {
		if hadURL {
			_ = os.Setenv("DATABASE_URL", oldURL)
		} else {
			_ = os.Unsetenv("DATABASE_URL")
		}
	})
	_ = os.Unsetenv("DATABASE_URL")
	t.Setenv("APP_ENV", "development")
	t.Setenv("HTTP_LISTEN_ADDR", address)
	t.Setenv("HTTP_SHUTDOWN_TIMEOUT", "2s")
	cmd.Env = append(os.Environ(), "FAQ_API_TEST_CHILD=1")
	var logs bytes.Buffer
	cmd.Stdout = &logs
	cmd.Stderr = &logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill() }()
	client := &http.Client{Timeout: 100 * time.Millisecond}
	ready := false
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); {
		response, err := client.Get("http://" + address + "/health/ready")
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == 200 {
				ready = true
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !ready {
		t.Fatal("server did not become ready")
	}
	if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("SIGTERM exit: %v\n%s", err, logs.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown timed out")
	}
	if !bytes.Contains(logs.Bytes(), []byte("API shutting down")) || !bytes.Contains(logs.Bytes(), []byte("API stopped")) {
		t.Fatal("shutdown lifecycle not logged")
	}
}
