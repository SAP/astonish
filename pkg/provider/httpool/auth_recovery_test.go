package httpool

import (
	"context"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

type authRoundTripper func(*http.Request) (*http.Response, error)

func (f authRoundTripper) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type authTrackedBody struct {
	io.Reader
	reads, closes int
}

func (b *authTrackedBody) Read(p []byte) (int, error) {
	b.reads++
	return b.Reader.Read(p)
}
func (b *authTrackedBody) Close() error { b.closes++; return nil }

func authDecorate(r *http.Request, token string) { r.Header.Set("Authorization", "Bearer "+token) }
func authRecover(status int) bool                { return status == http.StatusUnauthorized }
func authRequest(t *testing.T, body io.Reader) *http.Request {
	t.Helper()
	r, err := http.NewRequest(http.MethodPost, "https://example.test/chat", body)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestAuthRecoverySuccess(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusForbidden, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls := 0
			want := &http.Response{StatusCode: status, Body: http.NoBody}
			got, err := RoundTripWithToken(authRequest(t, nil), authRoundTripper(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Header.Get("Authorization") != "Bearer current" {
					t.Fatal("missing token decoration")
				}
				return want, nil
			}), func(_ context.Context, rejected string) (string, error) {
				if rejected != "" {
					t.Fatal("unexpected reactive acquisition")
				}
				return "current", nil
			}, func(string) { t.Fatal("unexpected invalidation") }, authDecorate, authRecover)
			if err != nil || got != want || calls != 1 {
				t.Fatalf("response=%p err=%v calls=%d", got, err, calls)
			}
		})
	}
}

func TestAuthRecoveryRenewAfterProactiveRefresh(t *testing.T) {
	r := authRequest(t, strings.NewReader("payload"))
	firstBody := &authTrackedBody{Reader: strings.NewReader("rejected")}
	final := &http.Response{StatusCode: http.StatusOK, Body: http.NoBody}
	var rejected, invalidated []string
	calls := 0
	got, err := RoundTripWithToken(r, authRoundTripper(func(r *http.Request) (*http.Response, error) {
		calls++
		data, err := io.ReadAll(r.Body)
		_ = r.Body.Close()
		if err != nil || string(data) != "payload" {
			t.Fatalf("body=%q err=%v", data, err)
		}
		if calls == 1 {
			if r.Header.Get("Authorization") != "Bearer proactive-refresh" {
				t.Fatal("wrong initial token")
			}
			return &http.Response{StatusCode: http.StatusUnauthorized, Body: firstBody}, nil
		}
		if r.Header.Get("Authorization") != "Bearer reactive-refresh" {
			t.Fatal("wrong retry token")
		}
		return final, nil
	}), func(_ context.Context, token string) (string, error) {
		rejected = append(rejected, token)
		if token == "" {
			return "proactive-refresh", nil
		}
		if firstBody.closes != 1 {
			t.Fatal("rejected response was not closed before acquisition")
		}
		return "reactive-refresh", nil
	}, func(token string) { invalidated = append(invalidated, token) }, authDecorate, authRecover)
	if err != nil || got != final || calls != 2 {
		t.Fatalf("response=%p err=%v calls=%d", got, err, calls)
	}
	if !reflect.DeepEqual(rejected, []string{"", "proactive-refresh"}) || !reflect.DeepEqual(invalidated, []string{"proactive-refresh"}) {
		t.Fatalf("acquire=%v invalidate=%v", rejected, invalidated)
	}
	if firstBody.reads != 0 {
		t.Fatal("read rejected response")
	}
}

func TestAuthRecoveryBounded(t *testing.T) {
	firstBody := &authTrackedBody{Reader: strings.NewReader("first")}
	lastBody := &authTrackedBody{Reader: strings.NewReader("last")}
	final := &http.Response{StatusCode: http.StatusUnauthorized, Body: lastBody}
	calls, acquisitions := 0, 0
	var invalidated []string
	got, err := RoundTripWithToken(authRequest(t, nil), authRoundTripper(func(*http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return &http.Response{StatusCode: http.StatusUnauthorized, Body: firstBody}, nil
		}
		return final, nil
	}), func(_ context.Context, token string) (string, error) {
		acquisitions++
		if token == "" {
			return "first", nil
		}
		return "second", nil
	}, func(token string) { invalidated = append(invalidated, token) }, authDecorate, authRecover)
	if err != nil || got != final || calls != 2 || acquisitions != 2 {
		t.Fatalf("err=%v calls=%d acquisitions=%d", err, calls, acquisitions)
	}
	if !reflect.DeepEqual(invalidated, []string{"first", "second"}) {
		t.Fatalf("invalidated=%v", invalidated)
	}
	if firstBody.closes != 1 || lastBody.closes != 0 || lastBody.reads != 0 {
		t.Fatal("incorrect response ownership")
	}
}

func TestAuthRecoveryBodyOwnership(t *testing.T) {
	t.Run("retry acquisition failure", func(t *testing.T) {
		initial := &authTrackedBody{Reader: strings.NewReader("payload")}
		retry := &authTrackedBody{Reader: strings.NewReader("payload")}
		r := authRequest(t, initial)
		r.GetBody = func() (io.ReadCloser, error) { return retry, nil }
		wantErr := errors.New("acquisition failed")
		calls := 0
		got, err := RoundTripWithToken(r, authRoundTripper(func(r *http.Request) (*http.Response, error) {
			calls++
			_ = r.Body.Close()
			return &http.Response{StatusCode: http.StatusUnauthorized, Body: http.NoBody}, nil
		}), func(_ context.Context, rejected string) (string, error) {
			if rejected == "" {
				return "first", nil
			}
			return "", wantErr
		}, func(string) {}, authDecorate, authRecover)
		if got != nil || err != wantErr || calls != 1 || initial.closes != 1 || retry.closes != 1 {
			t.Fatalf("err=%v calls=%d body closes=%d/%d", err, calls, initial.closes, retry.closes)
		}
	})
	t.Run("initial acquisition failure", func(t *testing.T) {
		body := &authTrackedBody{Reader: strings.NewReader("payload")}
		wantErr := errors.New("acquisition failed")
		_, err := RoundTripWithToken(authRequest(t, body), authRoundTripper(func(*http.Request) (*http.Response, error) {
			t.Fatal("transport must not run")
			return nil, nil
		}), func(context.Context, string) (string, error) { return "", wantErr }, func(string) {}, authDecorate, authRecover)
		if err != wantErr || body.closes != 1 {
			t.Fatalf("err=%v closes=%d", err, body.closes)
		}
	})
	for _, body := range []io.ReadCloser{nil, http.NoBody} {
		t.Run("empty body", func(t *testing.T) {
			r := authRequest(t, nil)
			r.Body = body
			calls := 0
			_, err := RoundTripWithToken(r, authRoundTripper(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Body != body {
					t.Fatal("empty body identity changed")
				}
				status := http.StatusOK
				if calls == 1 {
					status = http.StatusUnauthorized
				}
				return &http.Response{StatusCode: status, Body: http.NoBody}, nil
			}), func(context.Context, string) (string, error) { return "token", nil }, func(string) {}, authDecorate, authRecover)
			if err != nil || calls != 2 {
				t.Fatalf("err=%v calls=%d", err, calls)
			}
		})
	}
}

func TestAuthRecoveryUnreplayableBody(t *testing.T) {
	for _, mode := range []string{"missing GetBody", "GetBody failure", "nil GetBody result"} {
		t.Run(mode, func(t *testing.T) {
			body := &authTrackedBody{Reader: strings.NewReader("payload")}
			r := authRequest(t, body)
			if mode == "GetBody failure" {
				r.GetBody = func() (io.ReadCloser, error) { return nil, errors.New("private detail") }
			}
			if mode == "nil GetBody result" {
				r.GetBody = func() (io.ReadCloser, error) { return nil, nil }
			}
			rejectedBody := &authTrackedBody{Reader: strings.NewReader("private detail")}
			calls, acquisitions, invalidations := 0, 0, 0
			got, err := RoundTripWithToken(r, authRoundTripper(func(r *http.Request) (*http.Response, error) {
				calls++
				_ = r.Body.Close()
				return &http.Response{StatusCode: http.StatusUnauthorized, Body: rejectedBody}, nil
			}), func(context.Context, string) (string, error) { acquisitions++; return "rejected", nil }, func(token string) {
				invalidations++
				if token != "rejected" {
					t.Fatal("wrong token invalidated")
				}
			}, authDecorate, authRecover)
			if got != nil || !errors.Is(err, ErrAuthReplayUnavailable) || calls != 1 || acquisitions != 1 || invalidations != 1 {
				t.Fatalf("err=%v calls=%d acquire=%d invalidate=%d", err, calls, acquisitions, invalidations)
			}
			if body.reads != 0 || rejectedBody.reads != 0 || rejectedBody.closes != 1 {
				t.Fatal("body buffered or rejection leaked")
			}
		})
	}
}

func TestAuthRecoveryCancellation(t *testing.T) {
	for _, stage := range []string{"before acquisition", "initial acquisition", "retry acquisition"} {
		t.Run(stage, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			initial := &authTrackedBody{Reader: strings.NewReader("payload")}
			retry := &authTrackedBody{Reader: strings.NewReader("payload")}
			r := authRequest(t, initial).WithContext(ctx)
			r.GetBody = func() (io.ReadCloser, error) { return retry, nil }
			if stage == "before acquisition" {
				cancel()
			}
			calls := 0
			got, err := RoundTripWithToken(r, authRoundTripper(func(r *http.Request) (*http.Response, error) {
				calls++
				_ = r.Body.Close()
				return &http.Response{StatusCode: http.StatusUnauthorized, Body: http.NoBody}, nil
			}), func(ctx context.Context, rejected string) (string, error) {
				if stage == "initial acquisition" || rejected != "" {
					cancel()
					return "", errors.New("masked cancellation")
				}
				return "first", nil
			}, func(string) {}, authDecorate, authRecover)
			if got != nil || err != context.Canceled {
				t.Fatalf("response=%p err=%v", got, err)
			}
			wantCalls, wantRetryCloses := 0, 0
			if stage == "retry acquisition" {
				wantCalls, wantRetryCloses = 1, 1
			}
			if calls != wantCalls || initial.closes != 1 || retry.closes != wantRetryCloses {
				t.Fatalf("calls=%d closes=%d/%d", calls, initial.closes, retry.closes)
			}
		})
	}
}

func TestAuthRecoveryRequestImmutable(t *testing.T) {
	r := authRequest(t, nil)
	r.Header.Set("Authorization", "original")
	r.Header.Set("X-Original", "value")
	originalHeader := r.Header.Clone()
	originalURL := *r.URL
	calls := 0
	_, err := RoundTripWithToken(r, authRoundTripper(func(sent *http.Request) (*http.Response, error) {
		calls++
		if sent == r || sent.URL == r.URL {
			t.Fatal("request not cloned")
		}
		if sent.Header.Get("X-Original") != "value" {
			t.Fatal("retry cloned decorated request")
		}
		sent.Header.Set("X-Original", "mutated")
		sent.URL.Path = "/mutated"
		status := http.StatusOK
		if calls == 1 {
			status = http.StatusUnauthorized
		}
		return &http.Response{StatusCode: status, Body: http.NoBody}, nil
	}), func(context.Context, string) (string, error) { return "token", nil }, func(string) {}, authDecorate, authRecover)
	if err != nil || calls != 2 || !reflect.DeepEqual(r.Header, originalHeader) || *r.URL != originalURL {
		t.Fatal("original request mutated or recovery failed")
	}
}

func TestAuthRecoveryDoesNotReadSuccessBody(t *testing.T) {
	body := &authTrackedBody{Reader: strings.NewReader("stream")}
	want := &http.Response{StatusCode: http.StatusOK, Body: body}
	got, err := RoundTripWithToken(authRequest(t, nil), authRoundTripper(func(*http.Request) (*http.Response, error) { return want, nil }), func(context.Context, string) (string, error) { return "token", nil }, func(string) {}, authDecorate, authRecover)
	if err != nil || got != want || body.reads != 0 || body.closes != 0 {
		t.Fatal("successful stream consumed or closed")
	}
}
