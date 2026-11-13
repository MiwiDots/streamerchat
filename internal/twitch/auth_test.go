package twitch

import (
	"errors"
	"testing"
)

func TestAuthStatusErrClassification(t *testing.T) {
	for _, code := range []int{400, 401} {
		if !IsTokenRejected(authStatusErr("op", code)) {
			t.Errorf("%d must count as rejected", code)
		}
	}
	for _, code := range []int{429, 500, 502, 503} {
		if IsTokenRejected(authStatusErr("op", code)) {
			t.Errorf("%d is transient, must not count as rejected", code)
		}
	}
	if IsTokenRejected(errors.New("dial tcp: i/o timeout")) {
		t.Error("network errors are transient")
	}
}
