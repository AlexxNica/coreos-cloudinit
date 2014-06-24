package pkg

import (
	"crypto/tls"
	"errors"
	"fmt"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	neturl "net/url"
	"strings"
	"time"
)

const (
	HTTP_2xx = 2
	HTTP_4xx = 4
)

type Err error

type ErrTimeout struct{
	Err
}

type ErrNotFound struct{
	Err
}

type ErrInvalid struct{
	Err
}

type HttpClient struct {
	// Maximum exp backoff duration. Defaults to 5 seconds
	MaxBackoff time.Duration

	// Maximum number of connection retries. Defaults to 15
	MaxRetries int

	// HTTP client timeout, this is suggested to be low since exponential
	// backoff will kick off too. Defaults to 2 seconds
	Timeout time.Duration

	// Whether or not to skip TLS verification. Defaults to false
	SkipTLS bool
}

func NewHttpClient() *HttpClient {
	return &HttpClient{
		MaxBackoff: time.Second * 5,
		MaxRetries: 15,
		Timeout:    time.Duration(2) * time.Second,
		SkipTLS:    false,
	}
}

func expBackoff(interval, max time.Duration) time.Duration {
	interval = interval * 2
	if interval > max {
		interval = max
	}
	return interval
}

// Fetches a given URL with support for exponential backoff and maximum retries
func (h *HttpClient) Get(rawurl string) ([]byte, error) {
	if rawurl == "" {
		return nil, ErrInvalid{errors.New("URL is empty. Skipping.")}
	}

	url, err := neturl.Parse(rawurl)
	if err != nil {
		return nil, ErrInvalid{err}
	}

	// Unfortunately, url.Parse is too generic to throw errors if a URL does not
	// have a valid HTTP scheme. So, we have to do this extra validation
	if !strings.HasPrefix(url.Scheme, "http") {
		return nil, ErrInvalid{fmt.Errorf("URL %s does not have a valid HTTP scheme. Skipping.", rawurl)}
	}

	dataURL := url.String()

	// We need to create our own client in order to add timeout support.
	// TODO(c4milo) Replace it once Go 1.3 is officially used by CoreOS
	// More info: https://code.google.com/p/go/source/detail?r=ada6f2d5f99f
	transport := &http.Transport{
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: h.SkipTLS,
		},
		Dial: func(network, addr string) (net.Conn, error) {
			deadline := time.Now().Add(h.Timeout)
			c, err := net.DialTimeout(network, addr, h.Timeout)
			if err != nil {
				return nil, err
			}
			c.SetDeadline(deadline)
			return c, nil
		},
	}

	client := &http.Client{
		Transport: transport,
	}

	duration := 50 * time.Millisecond
	for retry := 1; retry <= h.MaxRetries; retry++ {
		log.Printf("Fetching data from %s. Attempt #%d", dataURL, retry)

		resp, err := client.Get(dataURL)

		if err == nil {
			defer resp.Body.Close()
			status := resp.StatusCode / 100

			if status == HTTP_2xx {
				return ioutil.ReadAll(resp.Body)
			}

			if status == HTTP_4xx {
				return nil, ErrNotFound{fmt.Errorf("Not found. HTTP status code: %d", resp.StatusCode)}
			}

			log.Printf("Server error. HTTP status code: %d", resp.StatusCode)
		} else {
			log.Printf("Unable to fetch data: %s", err.Error())
		}

		duration = expBackoff(duration, h.MaxBackoff)
		log.Printf("Sleeping for %v...", duration)
		time.Sleep(duration)
	}

	return nil, ErrTimeout{fmt.Errorf("Unable to fetch data. Maximum retries reached: %d", h.MaxRetries)}
}
