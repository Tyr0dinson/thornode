package endpoints

import (
	"net/url"
	"strings"
)

// redactedError carries a scrubbed message and still unwraps to the original so
// errors.Is / errors.As keep working. The original is never printed.
type redactedError struct {
	msg   string
	cause error
}

func (e *redactedError) Error() string { return e.msg }
func (e *redactedError) Unwrap() error { return e.cause }

// SecretsOf lists the strings that must never appear in logs for an endpoint: the
// whole URL, the URL without scheme, host plus path, the path, the userinfo
// password, the query, and any bearer token or basic password. Empty and trivially
// short values are dropped. Longest first so a substring is not scrubbed before its
// superstring.
func SecretsOf(u *url.URL, extra ...string) []string {
	var out []string
	add := func(s string) {
		if len(s) >= 4 {
			out = append(out, s)
		}
	}
	if u != nil {
		add(u.String())
		add(u.Redacted())
		add(strings.TrimPrefix(u.String(), u.Scheme+"://"))
		add(u.Host + u.Path)
		if u.Path != "" && u.Path != "/" {
			add(u.Path)
		}
		if u.User != nil {
			if pw, ok := u.User.Password(); ok {
				add(pw)
			}
		}
		add(u.RawQuery)
	}
	for _, s := range extra {
		add(s)
	}
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && len(out[j]) > len(out[j-1]); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

// Redact returns err with every secret replaced by "***". A nil err returns nil.
func Redact(err error, secrets []string) error {
	if err == nil {
		return nil
	}
	msg := err.Error()
	for _, s := range secrets {
		msg = strings.ReplaceAll(msg, s, "***")
	}
	return &redactedError{msg: msg, cause: err}
}
