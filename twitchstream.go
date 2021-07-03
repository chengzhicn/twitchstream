package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"log"
	"net"
	"net/http"
	_ "net/http/pprof"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// twitch didn't support HEAD, send range header without end will result in 200 and all data
// send range with start >= size will result in 416, otherwise 206 and send back data until size or range end whichever comes first
// for each chunk send 16 * 64k request, each request download to fixed position in buffer and with retry, send back error to error chan
// and send back wheather it's done

func doRequest(ctx context.Context, client *http.Client, method, url, rng string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (X11; Linux x86_64; rv:72.0) Gecko/20100101 Firefox/72.0")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Referer", "https://www.twitch.tv/")
	req.Header.Set("client-id", "jzkbprff40iqj646a697cyrvl0zt2m6")
	if rng != "" {
		req.Header.Set("Range", "bytes="+rng)
	}
	return client.Do(req)
}

func readRequest(rsp *http.Response, err error) (int, []byte, error) {
	if err != nil {
		return 0, nil, err
	}
	defer rsp.Body.Close()
	data, err := ioutil.ReadAll(rsp.Body)
	return rsp.StatusCode, data, err
}

func verifyRequest(status int, data []byte, err error) ([]byte, error) {
	if err != nil {
		return nil, err
	} else if status != 200 {
		return nil, fmt.Errorf("got unexpected status: %d", status)
	}
	return data, err
}

// if there is error, won't guaranteed to send to cm, but to cs
func downloadPart(ctx context.Context, client *http.Client, url string, b []byte, offset int, cm chan bool, cs chan int, ce chan error) {
	var start, n int
	var rsp *http.Response
	var err error
	for retry := 0; retry < 3 && start < len(b); retry++ {
		if rsp, err = doRequest(ctx, client, "GET", url, strconv.Itoa(offset+start)+"-"+strconv.Itoa(offset+len(b)-1)); err != nil {
			continue
		} else if rsp.StatusCode == http.StatusRequestedRangeNotSatisfiable {
			rsp.Body.Close()
			if cm != nil {
				cm <- false
			}
			break
		} else if rsp.StatusCode != http.StatusPartialContent {
			err = fmt.Errorf("got unexpected status: %d", rsp.StatusCode)
		} else if cm != nil {
			cm <- true
			cm = nil
		}
		for start < len(b) && err == nil {
			n, err = rsp.Body.Read(b[start:])
			start += n
		}
		rsp.Body.Close()
		if err == io.EOF {
			err = nil
			break
		}
	}
	cs <- start
	if err != nil {
		ce <- err
	}
}

func parallelDownload(ctx context.Context, client *http.Client, url string) (data []byte, err error) {
	const batch = 64 * 1024
	const count = 16
	var offset int
	var buffer [][]byte
	var cs []chan int
	var ce []chan error
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
loop:
	for i := 0; ; i++ {
		buffer = append(buffer, make([]byte, batch*count))
		cs = append(cs, make(chan int, count))
		ce = append(ce, make(chan error, count))
		var cm chan bool
		for j := 0; j < count; j++ {
			if j == count-1 {
				cm = make(chan bool, 1)
			}
			go downloadPart(ctx, client, url, buffer[i][j*batch:(j+1)*batch], offset, cm, cs[i], ce[i])
			offset += batch
		}
		select {
		case m := <-cm:
			if !m {
				break loop
			}
		case err = <-ce[i]:
			return
		}
	}
	for i := 0; i < len(cs); i++ {
		var s int
		for j := 0; j < count; {
			select {
			case n := <-cs[i]:
				j++
				s += n
			case err = <-ce[i]:
				return
			}
		}
		data = append(data, buffer[i][:s]...)
	}
	return
}

func m3uReader(r io.Reader) (list []string, skipped []string, err error) {
	scanner := bufio.NewScanner(r)
	discontinue := false
	for scanner.Scan() {
		if line := strings.TrimPrefix(scanner.Text(), "#EXT-X-TWITCH-PREFETCH:"); len(line) == 0 {
		} else if strings.HasPrefix(line, "#EXT-X-SCTE35-OUT") { // EXT-X-DISCONTINUITY flag is not reliable since there might be multiple ads in a row
			discontinue = true
		} else if strings.HasPrefix(line, "#EXT-X-SCTE35-IN") {
			discontinue = false
		} else if string(line[0]) == "#" {
		} else if discontinue {
			skipped = append(skipped, line)
		} else {
			list = append(list, line)
		}
	}
	return list, skipped, scanner.Err()
}

type TwitchStream struct {
	cc *http.Client
	dc *http.Client
}

func (ts *TwitchStream) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	sent := false
	defer func() {
		if !sent {
			w.WriteHeader(http.StatusBadGateway)
		}
	}()
	var auth map[string]interface{}
	var m3u string
	chunks := make(chan string, 32)
	out := make(chan chan []byte, 8)
	user := strings.TrimLeft(r.URL.RequestURI(), "/")
	if data, err := verifyRequest(readRequest(doRequest(r.Context(), ts.cc, "GET", "https://api.twitch.tv/api/channels/"+user+"/access_token?platform=ios", ""))); err != nil {
		log.Printf("user request %s: %s\n", user, err.Error())
		return
	} else if err = json.Unmarshal(data, &auth); err != nil {
		log.Printf("failed to decode user request %s: %s\n", user, err.Error())
		return
	} else if sig, ok := auth["sig"].(string); !ok {
		log.Printf("user request %s didn't have sig\n", user)
		return
	} else if token, ok := auth["token"].(string); !ok {
		log.Printf("user request %s didn't have token\n", user)
		return
	} else if data, err = verifyRequest(readRequest(doRequest(r.Context(), ts.dc, "GET", "https://usher.ttvnw.net/api/channel/hls/"+user+".m3u8?allow_source=true&fast_bread=true&sig="+sig+"&token="+url.QueryEscape(token), ""))); err != nil {
		log.Printf("playlist request %s: %s\n", user, err.Error())
		return
	} else if m3us, _, err := m3uReader(bytes.NewReader(data)); err != nil {
		log.Printf("failed to parse playlist %s: %s\n", user, err.Error())
		return
	} else if len(m3us) == 0 {
		log.Printf("empty playlist %s\n", user)
		return
	} else {
		m3u = m3us[0]
	}
	go func() {
		defer close(chunks)
		seen := make(map[string]bool)
		for {
			time.Sleep(1 * time.Second)
			var segs []string
			if status, data, err := readRequest(doRequest(r.Context(), ts.dc, "GET", m3u, "")); status == 404 || r.Context().Err() == context.Canceled {
				return
			} else if _, err = verifyRequest(status, data, err); err != nil {
				log.Printf("m3u request %s(%s): %s\n", user, m3u, err.Error())
				continue
			} else if segs, _, err = m3uReader(bytes.NewReader(data)); err != nil {
				log.Printf("failed to parse m3u %s(%s): %s\n", user, m3u, err.Error())
				return
			}
			for _, seg := range segs {
				if _, ok := seen[seg]; ok {
					continue
				}
				seen[seg] = true
				select {
				case chunks <- seg:
				default:
				loop:
					for {
						select {
						case <-chunks:
						default:
							break loop
						}
					}
					chunks <- seg
				}
			}
		}
	}()
	go func() {
		defer close(out)
		for chunk := range chunks {
			b := make(chan []byte, 1)
			go func(c string) {
				defer close(b)
				if data, err := parallelDownload(r.Context(), ts.dc, c); err == nil {
					b <- data
				}
			}(chunk)
			out <- b
		}
	}()
	defer func() {
		for range out {
		}
	}()
	sent = true
	w.Header().Set("Content-Type", "video/MP2T")
	for c := range out {
		for b := range c {
			if _, err := w.Write(b); err != nil {
				return
			}
		}
	}
}

func main() {
	var addr string
	var bindControl string
	var bindData string
	var debug string
	flag.StringVar(&addr, "a", ":7313", "listen address")
	flag.StringVar(&bindControl, "bc", "", "bind control address")
	flag.StringVar(&bindData, "bd", "", "bind data address")
	flag.StringVar(&debug, "d", "", "debug address")
	flag.Parse()
	if debug != "" {
		go func() {
			log.Printf("starting profiler: %s", debug)
			http.ListenAndServe(debug, nil)
		}()
	}
	log.Printf("disabling HTTP/2 because it's crawling under packet loss...\n")
	log.Fatal(http.ListenAndServe(addr, &TwitchStream{
		cc: &http.Client{
			Transport: &http.Transport{
				Proxy: http.ProxyFromEnvironment,
				DialContext: (&net.Dialer{
					LocalAddr: &net.TCPAddr{
						IP: net.ParseIP(bindControl), // empty bindAddr will return nil
					},
				}).DialContext,
			},
		},
		dc: &http.Client{
			Transport: &http.Transport{
				Proxy: http.ProxyFromEnvironment,
				DialContext: (&net.Dialer{
					LocalAddr: &net.TCPAddr{
						IP: net.ParseIP(bindData), // empty bindAddr will return nil
					},
				}).DialContext,
				MaxIdleConnsPerHost: 99999999,
				IdleConnTimeout:     300 * time.Second,
				TLSNextProto:        make(map[string]func(string, *tls.Conn) http.RoundTripper),
				//DisableKeepAlives: true, // there should be more heathy conn when bad conn become idle, so bad conn will be droped
			},
		},
	}))
}
