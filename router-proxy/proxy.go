package main

import (
	"fmt"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"
)

// Router accepts POST bodies up to 6 MiB including image attachments.
const maxRequestBytes = 6 * 1024 * 1024

type routerProxy struct {
	upstream *url.URL
	proxy    *httputil.ReverseProxy
}

func newRouterProxy(upstreamHost string, upstreamPort int) (*routerProxy, error) {
	upstream, err := url.Parse(fmt.Sprintf("http://%s:%d", upstreamHost, upstreamPort))
	if err != nil {
		return nil, fmt.Errorf("parse upstream URL: %w", err)
	}

	proxy := httputil.NewSingleHostReverseProxy(upstream)
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.Host = upstream.Host
	}
	proxy.Transport = &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
	proxy.ErrorHandler = func(w http.ResponseWriter, r *http.Request, err error) {
		http.Error(w, "upstream router unavailable", http.StatusBadGateway)
	}

	return &routerProxy{
		upstream: upstream,
		proxy:    proxy,
	}, nil
}

func (p *routerProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.ContentLength > maxRequestBytes {
		http.Error(w, "request entity too large", http.StatusRequestEntityTooLarge)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	p.proxy.ServeHTTP(w, r)
}

func (p *routerProxy) listenURL(listenHost string, listenPort int) string {
	return fmt.Sprintf("http://%s:%d", listenHost, listenPort)
}

func (p *routerProxy) upstreamURL() string {
	return p.upstream.String()
}
