package forward

import (
	"bytes"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gravitational/oxy/testutils"
	"github.com/gravitational/oxy/utils"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"golang.org/x/net/websocket"
	. "gopkg.in/check.v1"
)

func TestFwd(t *testing.T) { TestingT(t) }

type FwdSuite struct{}

var _ = Suite(&FwdSuite{})

// Makes sure hop-by-hop headers are removed
func (s *FwdSuite) TestForwardHopHeaders(c *C) {
	called := false
	var outHeaders http.Header
	var outHost, expectedHost string
	srv := testutils.NewHandler(func(w http.ResponseWriter, req *http.Request) {
		called = true
		outHeaders = req.Header
		outHost = req.Host
		w.Write([]byte("hello"))
	})
	defer srv.Close()

	f, err := New()
	c.Assert(err, IsNil)

	proxy := testutils.NewHandler(func(w http.ResponseWriter, req *http.Request) {
		req.URL = testutils.ParseURI(srv.URL)
		expectedHost = req.URL.Host
		f.ServeHTTP(w, req)
	})
	defer proxy.Close()

	headers := http.Header{
		Connection: []string{"close"},
		KeepAlive:  []string{"timeout=600"},
	}

	re, body, err := testutils.Get(proxy.URL, testutils.Headers(headers))
	c.Assert(err, IsNil)
	c.Assert(string(body), Equals, "hello")
	c.Assert(re.StatusCode, Equals, http.StatusOK)
	c.Assert(called, Equals, true)
	c.Assert(outHeaders.Get(Connection), Equals, "")
	c.Assert(outHeaders.Get(KeepAlive), Equals, "")
	c.Assert(outHost, Equals, expectedHost)
}

func (s *FwdSuite) TestDefaultErrHandler(c *C) {
	f, err := New()
	c.Assert(err, IsNil)

	proxy := testutils.NewHandler(func(w http.ResponseWriter, req *http.Request) {
		req.URL = testutils.ParseURI("http://localhost:63450")
		f.ServeHTTP(w, req)
	})
	defer proxy.Close()

	re, _, err := testutils.Get(proxy.URL)
	c.Assert(err, IsNil)
	c.Assert(re.StatusCode, Equals, http.StatusBadGateway)
}

func (s *FwdSuite) TestCustomErrHandler(c *C) {
	f, err := New(ErrorHandler(utils.ErrorHandlerFunc(func(w http.ResponseWriter, req *http.Request, err error) {
		w.WriteHeader(http.StatusTeapot)
		w.Write([]byte(http.StatusText(http.StatusTeapot)))
	})))
	c.Assert(err, IsNil)

	proxy := testutils.NewHandler(func(w http.ResponseWriter, req *http.Request) {
		req.URL = testutils.ParseURI("http://localhost:63450")
		f.ServeHTTP(w, req)
	})
	defer proxy.Close()

	re, body, err := testutils.Get(proxy.URL)
	c.Assert(err, IsNil)
	c.Assert(re.StatusCode, Equals, http.StatusTeapot)
	c.Assert(string(body), Equals, http.StatusText(http.StatusTeapot))
}

// Makes sure hop-by-hop headers are removed
func (s *FwdSuite) TestForwardedHeaders(c *C) {
	var outHeaders http.Header
	srv := testutils.NewHandler(func(w http.ResponseWriter, req *http.Request) {
		outHeaders = req.Header
		w.Write([]byte("hello"))
	})
	defer srv.Close()

	f, err := New(Rewriter(&HeaderRewriter{TrustForwardHeader: true, Hostname: "hello"}))
	c.Assert(err, IsNil)

	proxy := testutils.NewHandler(func(w http.ResponseWriter, req *http.Request) {
		req.URL = testutils.ParseURI(srv.URL)
		f.ServeHTTP(w, req)
	})
	defer proxy.Close()

	headers := http.Header{
		XForwardedProto:  []string{"httpx"},
		XForwardedFor:    []string{"192.168.1.1"},
		XForwardedServer: []string{"foobar"},
		XForwardedHost:   []string{"upstream-foobar"},
	}

	re, _, err := testutils.Get(proxy.URL, testutils.Headers(headers))
	c.Assert(err, IsNil)
	c.Assert(re.StatusCode, Equals, http.StatusOK)
	c.Assert(outHeaders.Get(XForwardedProto), Equals, "httpx")
	c.Assert(strings.Contains(outHeaders.Get(XForwardedFor), "192.168.1.1"), Equals, true)
	c.Assert(strings.Contains(outHeaders.Get(XForwardedHost), "upstream-foobar"), Equals, true)
	c.Assert(outHeaders.Get(XForwardedServer), Equals, "hello")
}

func (s *FwdSuite) TestCustomRewriter(c *C) {
	var outHeaders http.Header
	srv := testutils.NewHandler(func(w http.ResponseWriter, req *http.Request) {
		outHeaders = req.Header
		w.Write([]byte("hello"))
	})
	defer srv.Close()

	f, err := New(Rewriter(&HeaderRewriter{TrustForwardHeader: false, Hostname: "hello"}))
	c.Assert(err, IsNil)

	proxy := testutils.NewHandler(func(w http.ResponseWriter, req *http.Request) {
		req.URL = testutils.ParseURI(srv.URL)
		f.ServeHTTP(w, req)
	})
	defer proxy.Close()

	headers := http.Header{
		XForwardedProto: []string{"httpx"},
		XForwardedFor:   []string{"192.168.1.1"},
	}

	re, _, err := testutils.Get(proxy.URL, testutils.Headers(headers))
	c.Assert(err, IsNil)
	c.Assert(re.StatusCode, Equals, http.StatusOK)
	c.Assert(outHeaders.Get(XForwardedProto), Equals, "http")
	c.Assert(strings.Contains(outHeaders.Get(XForwardedFor), "192.168.1.1"), Equals, false)
}

func (s *FwdSuite) TestCustomTransportTimeout(c *C) {
	srv := testutils.NewHandler(func(w http.ResponseWriter, req *http.Request) {
		time.Sleep(20 * time.Millisecond)
		w.Write([]byte("hello"))
	})
	defer srv.Close()

	f, err := New(RoundTripper(
		&http.Transport{
			ResponseHeaderTimeout: 5 * time.Millisecond,
		}))
	c.Assert(err, IsNil)

	proxy := testutils.NewHandler(func(w http.ResponseWriter, req *http.Request) {
		req.URL = testutils.ParseURI(srv.URL)
		f.ServeHTTP(w, req)
	})
	defer proxy.Close()

	re, _, err := testutils.Get(proxy.URL)
	c.Assert(err, IsNil)
	c.Assert(re.StatusCode, Equals, http.StatusGatewayTimeout)
}

func (s *FwdSuite) TestCustomLogger(c *C) {
	srv := testutils.NewHandler(func(w http.ResponseWriter, req *http.Request) {
		w.Write([]byte("hello"))
	})
	defer srv.Close()

	buf := &bytes.Buffer{}
	l := utils.NewFileLogger(buf, utils.INFO)

	f, err := New(Logger(l))
	c.Assert(err, IsNil)

	proxy := testutils.NewHandler(func(w http.ResponseWriter, req *http.Request) {
		req.URL = testutils.ParseURI(srv.URL)
		f.ServeHTTP(w, req)
	})
	defer proxy.Close()

	re, _, err := testutils.Get(proxy.URL)
	c.Assert(err, IsNil)
	c.Assert(re.StatusCode, Equals, http.StatusOK)
	c.Assert(strings.Contains(buf.String(), srv.URL), Equals, true)
}

func TestRouteForwarding(t *testing.T) {
	var outPath string
	srv := testutils.NewHandler(func(w http.ResponseWriter, req *http.Request) {
		outPath = req.RequestURI
		_, _ = w.Write([]byte("hello"))
	})
	defer srv.Close()

	f, err := New()
	require.NoError(t, err)

	proxy := testutils.NewHandler(func(w http.ResponseWriter, req *http.Request) {
		req.URL = testutils.ParseURI(srv.URL)
		f.ServeHTTP(w, req)
	})
	defer proxy.Close()

	tests := []struct {
		Path  string
		Query string

		ExpectedPath string
	}{
		{"/hello", "", "/hello"},
		{"//hello", "", "//hello"},
		{"///hello", "", "///hello"},
		{"/hello?", "", "/hello"},
		{"/hello", "abc=def&def=123", "/hello?abc=def&def=123"},
		{"/log/http%3A%2F%2Fwww.site.com%2Fsomething?a=b", "", "/log/http%3A%2F%2Fwww.site.com%2Fsomething?a=b"},
	}

	for _, test := range tests {
		proxyURL := proxy.URL + test.Path
		if test.Query != "" {
			proxyURL = proxyURL + "?" + test.Query
		}
		request, err := http.NewRequest("GET", proxyURL, nil)
		require.NoError(t, err)

		re, err := http.DefaultClient.Do(request)
		require.NoError(t, err)
		assert.Equal(t, http.StatusOK, re.StatusCode)
		assert.Equal(t, test.ExpectedPath, outPath)
	}
}

func (s *FwdSuite) TestForwardedProto(c *C) {
	var proto string
	srv := testutils.NewHandler(func(w http.ResponseWriter, req *http.Request) {
		proto = req.Header.Get(XForwardedProto)
		w.Write([]byte("hello"))
	})
	defer srv.Close()

	buf := &bytes.Buffer{}
	l := utils.NewFileLogger(buf, utils.INFO)

	f, err := New(Logger(l))
	c.Assert(err, IsNil)

	proxy := http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		req.URL = testutils.ParseURI(srv.URL)
		f.ServeHTTP(w, req)
	})
	tproxy := httptest.NewUnstartedServer(proxy)
	tproxy.StartTLS()
	defer tproxy.Close()

	re, _, err := testutils.Get(tproxy.URL)
	c.Assert(err, IsNil)
	c.Assert(re.StatusCode, Equals, http.StatusOK)
	c.Assert(proto, Equals, "https")

	c.Assert(strings.Contains(buf.String(), "tls"), Equals, true)
}

func (s *FwdSuite) TestFlush(c *C) {
	srv := testutils.NewHandler(func(w http.ResponseWriter, req *http.Request) {
		h := w.(http.Hijacker)
		conn, _, _ := h.Hijack()
		defer conn.Close()
		data := "HTTP/1.1 200 OK\r\n" +
			"Transfer-Encoding: chunked\r\n" +
			"\r\n" +
			"0a\r\n" +
			"Body here\n\r\n"
		fmt.Fprintf(conn, data)
		time.Sleep(50 * time.Millisecond)
		data = "09\r\n" +
			"continued\r\n" +
			"0\r\n" +
			"\r\n"
		fmt.Fprintf(conn, data)
	})
	defer srv.Close()

	// Without flush interval this proxying fails, because client fails
	// to receive data on the wire before request closes
	f, err := New(FlushInterval(time.Millisecond))
	c.Assert(err, IsNil)

	proxy := testutils.NewHandler(func(w http.ResponseWriter, req *http.Request) {
		req.URL = testutils.ParseURI(srv.URL)
		f.ServeHTTP(w, req)
	})
	defer proxy.Close()

	request, err := http.NewRequest("GET", proxy.URL, nil)
	c.Assert(err, IsNil)
	re, err := http.DefaultClient.Do(request)
	c.Assert(err, IsNil)
	buffer := make([]byte, 4096)
loop:
	for {
		n, err := re.Body.Read(buffer)
		if n != 0 {
			val := string(buffer[:n])
			// found the frame
			if val == "continued" {
				break loop
			}
		}
		if err != nil {
			c.Fatalf("Timeout waiting for the frame to arrive: %v", err)
		}
	}
}

func (s *FwdSuite) TestChunkedResponseConversion(c *C) {
	srv := testutils.NewHandler(func(w http.ResponseWriter, req *http.Request) {
		h := w.(http.Hijacker)
		conn, _, _ := h.Hijack()
		data := "HTTP/1.1 200 OK\r\n" +
			"Transfer-Encoding: chunked\r\n" +
			"\r\n" +
			"0a\r\n" +
			"Body here\n\r\n" +
			"09\r\n" +
			"continued\r\n" +
			"0\r\n" +
			"\r\n"
		fmt.Fprintf(conn, data)
		conn.Close()
	})
	defer srv.Close()

	f, err := New()
	c.Assert(err, IsNil)

	proxy := testutils.NewHandler(func(w http.ResponseWriter, req *http.Request) {
		req.URL = testutils.ParseURI(srv.URL)
		f.ServeHTTP(w, req)
	})
	defer proxy.Close()

	re, body, err := testutils.Get(proxy.URL)
	c.Assert(err, IsNil)
	expected := "Body here\ncontinued"
	c.Assert(string(body), Equals, expected)
	c.Assert(re.StatusCode, Equals, http.StatusOK)
	c.Assert(re.Header.Get("Content-Length"), Equals, fmt.Sprintf("%d", len(expected)))
}

func (s *FwdSuite) TestDetectsWebsocketRequest(c *C) {
	mux := http.NewServeMux()
	mux.Handle("/ws", websocket.Handler(func(conn *websocket.Conn) {
		conn.Write([]byte("ok"))
		conn.Close()
	}))
	srv := testutils.NewHandler(func(w http.ResponseWriter, req *http.Request) {
		websocketRequest := isWebsocketRequest(req)
		c.Assert(websocketRequest, Equals, true)
		mux.ServeHTTP(w, req)
	})
	defer srv.Close()

	serverAddr := srv.Listener.Addr().String()
	resp, err := sendWebsocketRequest(serverAddr, "/ws", "echo", c)
	c.Assert(err, IsNil)
	c.Assert(resp, Equals, "ok")
}

func (s *FwdSuite) TestForwardsWebsocketTraffic(c *C) {
	f, err := New()
	c.Assert(err, IsNil)

	mux := http.NewServeMux()
	mux.Handle("/ws", websocket.Handler(func(conn *websocket.Conn) {
		conn.Write([]byte("ok"))
		conn.Close()
	}))
	srv := testutils.NewHandler(func(w http.ResponseWriter, req *http.Request) {
		mux.ServeHTTP(w, req)
	})
	defer srv.Close()

	proxy := testutils.NewHandler(func(w http.ResponseWriter, req *http.Request) {
		path := req.URL.Path // keep the original path
		// Set new backend URL
		req.URL = testutils.ParseURI(srv.URL)
		req.URL.Path = path
		f.ServeHTTP(w, req)
	})
	defer proxy.Close()

	proxyAddr := proxy.Listener.Addr().String()
	resp, err := sendWebsocketRequest(proxyAddr, "/ws", "echo", c)
	c.Assert(err, IsNil)
	c.Assert(resp, Equals, "ok")
}

func (s *FwdSuite) TestWebsocketFailedUpgrade(c *C) {
	f, err := New()
	c.Assert(err, IsNil)

	// Setup web server that always replies with access denied to websocket
	// upgrade requests and records received HTTP requests.
	reqCh := make(chan *http.Request, 2)
	srv := testutils.NewHandler(func(w http.ResponseWriter, req *http.Request) {
		reqCh <- req
		if req.URL.Path == "/ws" {
			http.Error(w, "access denied", http.StatusForbidden)
		} else {
			fmt.Fprint(w, "ok")
		}
	})
	defer srv.Close()

	// Setup web server that forwards all requests to the backend websocket
	// server configured above.
	proxy := testutils.NewHandler(func(w http.ResponseWriter, req *http.Request) {
		path := req.URL.Path
		req.URL = testutils.ParseURI(srv.URL)
		req.URL.Path = path
		f.ServeHTTP(w, req)
	})
	defer proxy.Close()

	proxyAddr := proxy.Listener.Addr().String()

	// Dial the proxy.
	conn, err := net.DialTimeout("tcp", proxyAddr, dialTimeout)
	c.Assert(err, IsNil)
	conn.SetDeadline(time.Now().Add(time.Second))

	// Send a websocket upgrade request which should fail.
	config := newWebsocketConfig(proxyAddr, "/ws")
	_, err = websocket.NewClient(config, conn)
	c.Assert(err, NotNil)

	// Make sure websocket upgrade request went through.
	select {
	case <-reqCh:
	case <-time.After(time.Second):
		c.Fatal("didn't receive websocket upgrade request")
	}

	// Send HTTP request on the *same* TCP connection and make sure
	// it doesn't go through.
	req, err := http.NewRequest("GET", "/", nil)
	c.Assert(err, IsNil)
	err = req.Write(conn)
	c.Assert(err, IsNil)

	// Make sure HTTP request didn't go through.
	select {
	case <-reqCh:
		c.Fatal("received HTTP request after websocket upgrade failure")
	case <-time.After(time.Second):
	}
}

const dialTimeout = time.Second

func sendWebsocketRequest(serverAddr, path, data string, c *C) (received string, err error) {
	client, err := net.DialTimeout("tcp", serverAddr, dialTimeout)
	if err != nil {
		return "", err
	}
	config := newWebsocketConfig(serverAddr, path)
	conn, err := websocket.NewClient(config, client)
	if err != nil {
		return "", err
	}
	defer conn.Close()
	if _, err := conn.Write([]byte(data)); err != nil {
		return "", err
	}
	var msg = make([]byte, 512)
	var n int
	n, err = conn.Read(msg)
	if err != nil {
		return "", err
	}

	received = string(msg[:n])
	return received, nil
}

func newWebsocketConfig(serverAddr, path string) *websocket.Config {
	config, _ := websocket.NewConfig(fmt.Sprintf("ws://%s%s", serverAddr, path), "http://localhost")
	return config
}
