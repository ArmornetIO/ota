package health

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestProbeURL(t *testing.T) {
	cases := []struct {
		code int
		body string
		want string
	}{
		{200, "", StatusOK},
		{503, "", StatusStarting},
		{500, "", StatusFatalError},
		{404, "", StatusRecoverableError},
		{503, `{"status":"FatalError"}`, StatusFatalError},
		{200, `{"status":"Starting"}`, StatusStarting},
		{200, `{"status":"made-up"}`, StatusOK},
	}
	for _, c := range cases {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(c.code)
			_, _ = w.Write([]byte(c.body))
		}))
		got, err := ProbeURL(srv.URL).Status(context.Background())
		srv.Close()
		if err != nil || got != c.want {
			t.Errorf("code %d body %q: got %q, %v; want %q", c.code, c.body, got, err, c.want)
		}
	}
}

func TestProbeURLNotListeningIsStarting(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()
	if got, _ := ProbeURL(url).Status(context.Background()); got != StatusStarting {
		t.Fatalf("got %q, want Starting", got)
	}
}

func TestStatusFuncAndCommitOnly(t *testing.T) {
	if got, _ := StatusFunc(func() string { return StatusOK }).Status(context.Background()); got != StatusOK {
		t.Fatalf("StatusFunc = %q", got)
	}
	if _, err := CommitOnly().Status(context.Background()); !errors.Is(err, ErrCommitOnly) {
		t.Fatalf("CommitOnly err = %v", err)
	}
}
