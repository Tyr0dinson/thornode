package notify

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"

	. "gopkg.in/check.v1"

	"gitlab.com/thorchain/thornode/v3/tools/events/pkg/config"
	"gitlab.com/thorchain/thornode/v3/tools/events/pkg/util"
)

type NotifyCovSuite struct {
	orig config.Config
}

var _ = Suite(&NotifyCovSuite{})

func (s *NotifyCovSuite) SetUpTest(c *C) {
	s.orig = config.Get()
	cfg := config.Get()
	cfg.MaxRetries = 0
	config.Set(cfg)
}

func (s *NotifyCovSuite) TearDownTest(c *C) {
	config.Set(s.orig)
}

type recorderCov struct {
	mu     sync.Mutex
	bodies []map[string]any
	status int
}

func (r *recorderCov) server() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		raw, _ := io.ReadAll(req.Body)
		body := map[string]any{}
		_ = json.Unmarshal(raw, &body)
		r.mu.Lock()
		r.bodies = append(r.bodies, body)
		r.mu.Unlock()
		w.WriteHeader(r.status)
	}))
}

func (s *NotifyCovSuite) TestNotifyAllWebhooks(c *C) {
	slackRec := &recorderCov{status: http.StatusOK}
	slackSrv := slackRec.server()
	defer slackSrv.Close()
	discordRec := &recorderCov{status: http.StatusNoContent}
	discordSrv := discordRec.server()
	defer discordSrv.Close()

	cfg := config.Get()
	cfg.Console = true
	config.Set(cfg)

	lines := []string{"hello [link](https://example.com)", "second"}
	fields := util.NewOrderedMap()
	fields.Set("Key", "value")

	Notify(config.Webhooks{
		Category:  "Cov",
		Slack:     slackSrv.URL,
		Discord:   discordSrv.URL,
		PagerDuty: "http://pagerduty.invalid",
	}, "my title", 55, lines, Info, fields)

	// caller's lines are not modified
	c.Assert(lines[0], Equals, "hello [link](https://example.com)")

	c.Assert(slackRec.bodies, HasLen, 1)
	text, _ := slackRec.bodies[0]["text"].(string)
	c.Assert(strings.Contains(text, "`[55]` my title"), Equals, true)
	c.Assert(strings.Contains(text, "<https://example.com|link>"), Equals, true)

	c.Assert(discordRec.bodies, HasLen, 1)
	raw, err := json.Marshal(discordRec.bodies[0])
	c.Assert(err, IsNil)
	c.Assert(strings.Contains(string(raw), "my title"), Equals, true)
}

func (s *NotifyCovSuite) TestNotifySlackFailurePanics(c *C) {
	rec := &recorderCov{status: http.StatusInternalServerError}
	srv := rec.server()
	defer srv.Close()

	c.Assert(func() {
		Notify(config.Webhooks{Slack: srv.URL}, "t", 1, nil, Info, util.NewOrderedMap())
	}, PanicMatches, ".*unable to send slack notification.*")
	c.Assert(rec.bodies, HasLen, 1)
}

func (s *NotifyCovSuite) TestNotifyDiscordFailurePanics(c *C) {
	rec := &recorderCov{status: http.StatusInternalServerError}
	srv := rec.server()
	defer srv.Close()

	c.Assert(func() {
		Notify(config.Webhooks{Discord: srv.URL}, "t", 1, nil, Info, util.NewOrderedMap())
	}, PanicMatches, ".*unable to send discord notification.*")
	c.Assert(rec.bodies, HasLen, 1)
}

func (s *NotifyCovSuite) TestNonMainnetQueryParamsStagenet(c *C) {
	cfg := config.Get()
	cfg.Network = "stagenet"
	cfg.Links.Explorer = "https://explorer.cov"
	cfg.Links.Track = "https://track.cov"
	config.Set(cfg)

	msg := "see https://explorer.cov/tx/ABC and [t](https://track.cov/x/1) and https://other.cov/y"
	got := nonMainnetQueryParams(msg)
	c.Assert(got, Equals,
		"see https://explorer.cov/tx/ABC?network=stagenet and [t](https://track.cov/x/1?network=stagenet) and https://other.cov/y")

	// empty network is treated like mainnet
	cfg.Network = ""
	config.Set(cfg)
	c.Assert(nonMainnetQueryParams(msg), Equals, msg)
}
