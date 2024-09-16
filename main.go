package main

import (
	"flag"
	"fmt"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
)

type Backend struct {
	URL          *url.URL
	Alive        bool
	mux          sync.RWMutex
	ReverseProxy *httputil.ReverseProxy
}

type ServerPool struct {
	backends []*Backend
	current  uint64
}

// method to get next index atomically (preventing issues with concurrency)
// could also lock and unlock the mux but this is better
func (s *ServerPool) NextIndex() int {
	return int(atomic.AddUint64(&s.current, uint64(1)) % uint64(len(s.backends)))
}

// returns next active backend to take a connection
func (s *ServerPool) GetNext() *Backend {
	next := s.NextIndex()
	end := next + len(s.backends)
	for i := next; i < end; i++ {
		index := i % len(s.backends)
		if s.backends[index].IsAlive() {
			if i != next {
				atomic.StoreUint64(&s.current, uint64(index))
			}
			return s.backends[index]
		}
	}
	return nil
}

func (s *ServerPool) AddBackend(b *Backend) {
	s.backends = append(s.backends, b)
}

// backend methods (must be serializable to avoid race conditions)
// to learn how mux works (https://medium.com/bootdotdev/golang-mutexes-what-is-rwmutex-for-5360ab082626)
func (b *Backend) SetAlive(alive bool) {
	b.mux.Lock()
	b.Alive = alive
	b.mux.Unlock()
}

func (b *Backend) IsAlive() bool {
	b.mux.RLock()
	alive := b.Alive
	b.mux.RUnlock()
	return alive
}

func LoadBalancer(w http.ResponseWriter, r *http.Request) {
	server := serverPool.GetNext()
}

var serverPool ServerPool

func main() {
	var serverList string
	var port int

	// command line args
	flag.StringVar(&serverList, "backends", "", "Backends (use commas to separate)")
	flag.IntVar(&port, "port", 3000, "Port to serve")
	flag.Parse()

	if len(serverList) == 0 {
		log.Fatal("Must have some backends")
	}

	tokens := strings.Split(serverList, ",")
	for _, tok := range tokens {
		serverUrl, err := url.Parse(tok)
		if err != nil {
			log.Fatal(err)
		}

		// reverse proxy directs client request to respective backend server
		proxy := httputil.NewSingleHostReverseProxy(serverUrl)

		// proxy takes a callback error function
		// we can use this to retry a connection
		proxy.ErrorHandler = func(writer http.ResponseWriter, request *http.Request, e error) {
			log.Printf("[%s] %s\n", serverUrl.Host, e.Error())
			retries := GetRetryFromContext(request)
			if retries < 3 {

			}
		}

		b := Backend{
			URL:          serverUrl,
			Alive:        true,
			ReverseProxy: proxy,
		}
		serverPool.AddBackend(&b)
	}

	server := http.Server{
		Addr:    fmt.Sprintf(":%d", port),
		Handler: http.HandlerFunc(loadBalancer),
	}

	go HealthCheck()

	log.Printf("Load balancer at :%d\n", port)
	if err := server.ListenAndServe(); err != nil {
		log.Fatal(err)
	}
}
